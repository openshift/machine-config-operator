package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleStreamJSON mirrors the real shape of openshift/installer's coreos-rhel-10.json.
const sampleStreamJSON = `{
  "stream": "rhcos-4.22",
  "architectures": {
    "aarch64": {
      "artifacts": {
        "aws": {
          "release": "10.2.20260423-0"
        }
      }
    },
    "x86_64": {
      "artifacts": {
        "aws": {
          "release": "10.2.20260423-0"
        }
      }
    }
  }
}`

func TestParseStreamCeiling(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		streamArch  string
		wantToken   string
		wantFullVer string
		expectError bool
	}{
		{
			name:        "x86_64",
			body:        sampleStreamJSON,
			streamArch:  "x86_64",
			wantToken:   "10.2",
			wantFullVer: "10.2.20260423-0",
		},
		{
			name:        "aarch64",
			body:        sampleStreamJSON,
			streamArch:  "aarch64",
			wantToken:   "10.2",
			wantFullVer: "10.2.20260423-0",
		},
		{
			name:        "unknown arch",
			body:        sampleStreamJSON,
			streamArch:  "s390x",
			expectError: true,
		},
		{
			name:        "malformed json",
			body:        "not json",
			streamArch:  "x86_64",
			expectError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, fullRelease, err := parseStreamCeiling([]byte(tc.body), tc.streamArch)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantToken, token)
			assert.Equal(t, tc.wantFullVer, fullRelease)
		})
	}
}

func TestReleaseToken(t *testing.T) {
	cases := []struct {
		name        string
		release     string
		want        string
		expectError bool
	}{
		{"standard release string", "10.2.20260423-0", "10.2", false},
		{"double digit minor", "9.10.20260210-0", "9.10", false},
		{"two segment string", "9.6", "9.6", false},
		{"empty string", "", "", true},
		{"single segment", "9", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := releaseToken(tc.release)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
