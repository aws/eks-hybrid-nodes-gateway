package vxlan_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws/hybrid-gateway/internal/vxlan"
)

func TestCheckIPForwarding(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		exists      bool
		expectedErr bool
	}{
		{
			name:    "enabled",
			content: "1\n",
			exists:  true,
		},
		{
			name:        "disabled",
			content:     "0\n",
			exists:      true,
			expectedErr: true,
		},
		{
			name:    "enabled without newline",
			content: "1",
			exists:  true,
		},
		{
			name:        "file does not exist",
			exists:      false,
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ip_forward")
			if tt.exists {
				err := os.WriteFile(path, []byte(tt.content), 0o644)
				assert.NoError(t, err)
			}
			err := vxlan.CheckIPForwarding(path)
			if tt.expectedErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
