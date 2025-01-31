package docker

import (
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"github.com/avatag-host/claws/api"
	"github.com/avatag-host/claws/environment"
	"github.com/avatag-host/claws/events"
	"io"
	"sync"
)

type Metadata struct {
	Image string
	Stop  api.ProcessStopConfiguration
}

// Ensure that the Docker environment is always implementing all of the methods
// from the base environment interface.
var _ environment.ProcessEnvironment = (*Environment)(nil)

type Environment struct {
	mu      sync.RWMutex
	eventMu sync.Mutex

	// The public identifier for this environment. In this case it is the Docker container
	// name that will be used for all instances created under it.
	Id string

	// The environment configuration.
	Configuration *environment.Configuration

	meta *Metadata

	// The Docker client being used for this instance.
	client *client.Client

	// Controls the hijacked response stream which exists only when we're attached to
	// the running container instance.
	stream *types.HijackedResponse

	// Holds the stats stream used by the polling commands so that we can easily close it out.
	stats io.ReadCloser

	emitter *events.EventBus

	// Tracks the environment state.
	st   string
	stMu sync.RWMutex
}

// Creates a new base Docker environment. The ID passed through will be the ID that is used to
// reference the container from here on out. This should be unique per-server (we use the UUID
// by default). The container does not need to exist at this point.
func New(id string, m *Metadata, c *environment.Configuration) (*Environment, error) {
	cli, err := environment.DockerClient()
	if err != nil {
		return nil, err
	}

	e := &Environment{
		Id:            id,
		Configuration: c,
		meta:          m,
		client:        cli,
		st:            environment.ProcessOfflineState,
	}

	return e, nil
}

func (e *Environment) Type() string {
	return "docker"
}

// Set if this process is currently attached to the process.
func (e *Environment) SetStream(s *types.HijackedResponse) {
	e.mu.Lock()
	e.stream = s
	e.mu.Unlock()
}

// Determine if the this process is currently attached to the container.
func (e *Environment) IsAttached() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.stream != nil
}

func (e *Environment) Events() *events.EventBus {
	e.eventMu.Lock()
	defer e.eventMu.Unlock()

	if e.emitter == nil {
		e.emitter = events.New()
	}

	return e.emitter
}

// Determines if the container exists in this environment. The ID passed through should be the
// server UUID since containers are created utilizing the server UUID as the name and docker
// will work fine when using the container name as the lookup parameter in addition to the longer
// ID auto-assigned when the container is created.
func (e *Environment) Exists() (bool, error) {
	_, err := e.client.ContainerInspect(context.Background(), e.Id)

	if err != nil {
		// If this error is because the container instance wasn't found via Docker we
		// can safely ignore the error and just return false.
		if client.IsErrNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// Determines if the server's docker container is currently running. If there is no container
// present, an error will be raised (since this shouldn't be a case that ever happens under
// correctly developed circumstances).
//
// You can confirm if the instance wasn't found by using client.IsErrNotFound from the Docker
// API.
//
// @see docker/client/errors.go
func (e *Environment) IsRunning() (bool, error) {
	c, err := e.client.ContainerInspect(context.Background(), e.Id)
	if err != nil {
		return false, err
	}

	return c.State.Running, nil
}

// Determine the container exit state and return the exit code and whether or not
// the container was killed by the OOM killer.
func (e *Environment) ExitState() (uint32, bool, error) {
	c, err := e.client.ContainerInspect(context.Background(), e.Id)
	if err != nil {
		// I'm not entirely sure how this can happen to be honest. I tried deleting a
		// container _while_ a server was running and wings gracefully saw the crash and
		// created a new container for it.
		//
		// However, someone reported an error in Discord about this scenario happening,
		// so I guess this should prevent it? They didn't tell me how they caused it though
		// so that's a mystery that will have to go unsolved.
		//
		// @see https://github.com/pterodactyl/panel/issues/2003
		if client.IsErrNotFound(err) {
			return 1, false, nil
		}

		return 0, false, errors.WithStack(err)
	}

	return uint32(c.State.ExitCode), c.State.OOMKilled, nil
}

// Returns the environment configuration allowing a process to make modifications of the
// environment on the fly.
func (e *Environment) Config() *environment.Configuration {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.Configuration
}

// Sets the stop configuration for the environment.
func (e *Environment) SetStopConfiguration(c api.ProcessStopConfiguration) {
	e.mu.Lock()
	e.meta.Stop = c
	e.mu.Unlock()
}

func (e *Environment) SetImage(i string) {
	e.mu.Lock()
	e.meta.Image = i
	e.mu.Unlock()
}
