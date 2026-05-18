package internalreleaseimage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenShiftTLSVersionToRegistryVersion(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "TLS 1.0",
			input:    "VersionTLS10",
			expected: "tls1.0",
		},
		{
			name:     "TLS 1.1",
			input:    "VersionTLS11",
			expected: "tls1.1",
		},
		{
			name:     "TLS 1.2",
			input:    "VersionTLS12",
			expected: "tls1.2",
		},
		{
			name:     "TLS 1.3",
			input:    "VersionTLS13",
			expected: "tls1.3",
		},
		{
			name:     "future TLS 1.4",
			input:    "VersionTLS14",
			expected: "tls1.4",
		},
		{
			name:     "empty string defaults to tls1.2",
			input:    "",
			expected: "tls1.2",
		},
		{
			name:     "invalid prefix defaults to tls1.2",
			input:    "SomethingElse",
			expected: "tls1.2",
		},
		{
			name:     "prefix only defaults to tls1.2",
			input:    "VersionTLS",
			expected: "tls1.2",
		},
		{
			name:     "single digit defaults to tls1.2",
			input:    "VersionTLS1",
			expected: "tls1.2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, openShiftTLSVersionToRegistryVersion(tc.input))
		})
	}
}
