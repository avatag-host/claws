package router

import (
	"bytes"
	"context"
	"github.com/apex/log"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/avatag-host/claws/server"
	"net/http"
	"os"
	"strconv"
)

type serverProcData struct {
	server.ResourceUsage
	Suspended bool `json:"suspended"`
}

// Returns a single server from the collection of servers.
func getServer(c *gin.Context) {
	s := GetServer(c.Param("server"))

	c.JSON(http.StatusOK, serverProcData{
		ResourceUsage: *s.Proc(),
		Suspended:     s.IsSuspended(),
	})
}

// Returns the logs for a given server instance.
func getServerLogs(c *gin.Context) {
	s := GetServer(c.Param("server"))

	l, _ := strconv.Atoi(c.DefaultQuery("size", "100"))
	if l <= 0 {
		l = 100
	} else if l > 100 {
		l = 100
	}

	out, err := s.ReadLogfile(l)
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": out})
}

// Handles a request to control the power state of a server. If the action being passed
// through is invalid a 404 is returned. Otherwise, a HTTP/202 Accepted response is returned
// and the actual power action is run asynchronously so that we don't have to block the
// request until a potentially slow operation completes.
//
// This is done because for the most part the Panel is using websockets to determine when
// things are happening, so theres no reason to sit and wait for a request to finish. We'll
// just see over the socket if something isn't working correctly.
func postServerPower(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		Action server.PowerAction `json:"action"`
	}

	if err := c.BindJSON(&data); err != nil {
		return
	}

	if !data.Action.IsValid() {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": "The power action provided was not valid, should be one of \"stop\", \"start\", \"restart\", \"kill\"",
		})
		return
	}

	// Because we route all of the actual bootup process to a separate thread we need to
	// check the suspension status here, otherwise the user will hit the endpoint and then
	// just sit there wondering why it returns a success but nothing actually happens.
	//
	// We don't really care about any of the other actions at this point, they'll all result
	// in the process being stopped, which should have happened anyways if the server is suspended.
	if (data.Action == server.PowerActionStart || data.Action == server.PowerActionRestart) && s.IsSuspended() {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "Cannot start or restart a server that is suspended.",
		})
		return
	}

	// Pass the actual heavy processing off to a separate thread to handle so that
	// we can immediately return a response from the server. Some of these actions
	// can take quite some time, especially stopping or restarting.
	go func(s *server.Server) {
		if err := s.HandlePowerAction(data.Action, 30); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				s.Log().WithField("action", data.Action).
					Warn("could not acquire a lock while attempting to perform a power action")
			} else {
				s.Log().WithFields(log.Fields{"action": data, "error": err}).
					Error("encountered error processing a server power action in the background")
			}
		}
	}(s)

	c.Status(http.StatusAccepted)
}

// Sends an array of commands to a running server instance.
func postServerCommands(c *gin.Context) {
	s := GetServer(c.Param("server"))

	if running, err := s.Environment.IsRunning(); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	} else if !running {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{
			"error": "Cannot send commands to a stopped server instance.",
		})
		return
	}

	var data struct {
		Commands []string `json:"commands"`
	}
	// BindJSON sends 400 if the request fails, all we need to do is return
	if err := c.BindJSON(&data); err != nil {
		return
	}

	for _, command := range data.Commands {
		if err := s.Environment.SendCommand(command); err != nil {
			s.Log().WithFields(log.Fields{"command": command, "error": err}).Warn("failed to send command to server instance")
		}
	}

	c.Status(http.StatusNoContent)
}

// Updates information about a server internally.
func patchServer(c *gin.Context) {
	s := GetServer(c.Param("server"))

	buf := bytes.Buffer{}
	buf.ReadFrom(c.Request.Body)

	if err := s.UpdateDataStructure(buf.Bytes()); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	s.SyncWithEnvironment()

	c.Status(http.StatusNoContent)
}

// Performs a server installation in a background thread.
func postServerInstall(c *gin.Context) {
	s := GetServer(c.Param("server"))

	go func(serv *server.Server) {
		if err := serv.Install(true); err != nil {
			serv.Log().WithField("error", err).Error("failed to execute server installation process")
		}
	}(s)

	c.Status(http.StatusAccepted)
}

// Reinstalls a server.
func postServerReinstall(c *gin.Context) {
	s := GetServer(c.Param("server"))

	if s.ExecutingPowerAction() {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": "Cannot execute server reinstall event while another power action is running.",
		})
		return
	}

	go func(s *server.Server) {
		if err := s.Reinstall(); err != nil {
			s.Log().WithField("error", err).Error("failed to complete server re-install process")
		}
	}(s)

	c.Status(http.StatusAccepted)
}

// Deletes a server from the wings daemon and dissociate it's objects.
func deleteServer(c *gin.Context) {
	s := GetServer(c.Param("server"))

	// Immediately suspend the server to prevent a user from attempting
	// to start it while this process is running.
	s.Config().SetSuspended(true)

	// If the server is currently installing, abort it.
	if s.IsInstalling() {
		s.AbortInstallation()
	}

	// Delete the server's archive if it exists. We intentionally don't return
	// here, if the archive fails to delete, the server can still be removed.
	if err := s.Archiver.DeleteIfExists(); err != nil {
		s.Log().WithField("error", err).Warn("failed to delete server archive during deletion process")
	}

	// Unsubscribe all of the event listeners.
	s.Events().Destroy()
	s.Throttler().StopTimer()
	s.Websockets().CancelAll()

	// Destroy the environment; in Docker this will handle a running container and
	// forcibly terminate it before removing the container, so we do not need to handle
	// that here.
	if err := s.Environment.Destroy(); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
	}

	// Once the environment is terminated, remove the server files from the system. This is
	// done in a separate process since failure is not the end of the world and can be
	// manually cleaned up after the fact.
	//
	// In addition, servers with large amounts of files can take some time to finish deleting
	// so we don't want to block the HTTP call while waiting on this.
	go func(p string) {
		if err := os.RemoveAll(p); err != nil {
			log.WithFields(log.Fields{
				"path":  p,
				"error": errors.WithStack(err),
			}).Warn("failed to remove server files during deletion process")
		}
	}(s.Filesystem().Path())

	var uuid = s.Id()
	server.GetServers().Remove(func(s2 *server.Server) bool {
		return s2.Id() == uuid
	})

	// Deallocate the reference to this server.
	s = nil

	c.Status(http.StatusNoContent)
}
