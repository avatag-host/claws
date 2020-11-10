package server

import (
	"github.com/avatag-host/claws/events"
)

// Defines all of the possible output events for a server.
// noinspection GoNameStartsWithPackageName
const (
	DaemonMessageEvent    = "daemon message"
	InstallOutputEvent    = "install output"
	InstallStartedEvent   = "install started"
	InstallCompletedEvent = "install completed"
	ConsoleOutputEvent    = "console output"
	StatusEvent           = "status"
	StatsEvent            = "stats"
	BackupCompletedEvent  = "backup completed"
)

// Returns the server's emitter instance.
func (s *Server) Events() *events.EventBus {
	s.emitterLock.Lock()
	defer s.emitterLock.Unlock()

	if s.emitter == nil {
		s.emitter = events.New()
	}

	return s.emitter
}
