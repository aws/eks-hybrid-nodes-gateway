package aws_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	hybridaws "github.com/aws/hybrid-gateway/internal/aws"
)

func TestParseRouteTableIDs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "single ID",
			input:    "rtb-12345",
			expected: []string{"rtb-12345"},
		},
		{
			name:     "multiple IDs",
			input:    "rtb-111,rtb-222,rtb-333",
			expected: []string{"rtb-111", "rtb-222", "rtb-333"},
		},
		{
			name:     "trims whitespace",
			input:    " rtb-111 , rtb-222 , rtb-333 ",
			expected: []string{"rtb-111", "rtb-222", "rtb-333"},
		},
		{
			name:     "skips empty entries",
			input:    "rtb-111,,rtb-222,",
			expected: []string{"rtb-111", "rtb-222"},
		},
		{
			name:     "empty string returns nil",
			input:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hybridaws.ParseRouteTableIDs(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
