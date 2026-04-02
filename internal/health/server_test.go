package health_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/hybrid-gateway/internal/health"
)

func TestNewServer(t *testing.T) {
	s := health.NewServer()
	assert.NoError(t, s.HealthCheck(nil), "new server should start healthy")
	assert.Error(t, s.ReadyCheck(nil), "new server should start not ready")
}

func TestHealthCheck(t *testing.T) {
	tests := []struct {
		name        string
		healthy     bool
		expectedErr bool
	}{
		{name: "healthy", healthy: true, expectedErr: false},
		{name: "unhealthy", healthy: false, expectedErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := health.NewServer()
			s.SetHealthy(tt.healthy)
			err := s.HealthCheck(nil)
			if tt.expectedErr {
				assert.Error(t, err)
				assert.Equal(t, "not healthy", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReadyCheck(t *testing.T) {
	tests := []struct {
		name        string
		ready       bool
		expectedErr bool
	}{
		{name: "ready", ready: true, expectedErr: false},
		{name: "not ready", ready: false, expectedErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := health.NewServer()
			s.SetReady(tt.ready)
			err := s.ReadyCheck(nil)
			if tt.expectedErr {
				assert.Error(t, err)
				assert.Equal(t, "not ready", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHealthStateTransitions(t *testing.T) {
	s := health.NewServer()

	s.SetHealthy(false)
	assert.Error(t, s.HealthCheck(nil))

	s.SetHealthy(true)
	assert.NoError(t, s.HealthCheck(nil), "should recover from unhealthy")

	s.SetReady(true)
	assert.NoError(t, s.ReadyCheck(nil))

	s.SetReady(false)
	assert.Error(t, s.ReadyCheck(nil), "should transition back to not ready")
}
