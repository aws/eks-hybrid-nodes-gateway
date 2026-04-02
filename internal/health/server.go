package health

import (
	"net/http"
	"sync/atomic"
)

// Server provides health and readiness checks for Kubernetes probes.
type Server struct {
	healthy atomic.Bool
	ready   atomic.Bool
}

func NewServer() *Server {
	s := &Server{}
	s.healthy.Store(true)
	return s
}

func (s *Server) SetReady(ready bool)     { s.ready.Store(ready) }
func (s *Server) SetHealthy(healthy bool) { s.healthy.Store(healthy) }

// HealthCheck implements the controller-runtime health check interface.
func (s *Server) HealthCheck(_ *http.Request) error {
	if !s.healthy.Load() {
		return &unhealthyError{}
	}
	return nil
}

// ReadyCheck implements the controller-runtime readiness check interface.
func (s *Server) ReadyCheck(_ *http.Request) error {
	if !s.ready.Load() {
		return &notReadyError{}
	}
	return nil
}

type unhealthyError struct{}

func (e *unhealthyError) Error() string { return "not healthy" }

type notReadyError struct{}

func (e *notReadyError) Error() string { return "not ready" }
