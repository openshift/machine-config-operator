package secrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretMerger(t *testing.T) {
	testCases := []struct {
		name           string
		input1         DockerConfigJSON
		input2         DockerConfigJSON
		expectedOutput DockerConfigJSON
	}{
		{
			name: "Simple concatenation",
			input1: DockerConfigJSON{
				Auths: map[string]DockerConfigEntry{
					"registry.hostname.com": DockerConfigEntry{
						Username: "user",
						Password: "p455w0rd",
					},
				},
			},
			input2: DockerConfigJSON{
				Auths: map[string]DockerConfigEntry{
					"second-registry.hostname.com": DockerConfigEntry{
						Username: "user",
						Password: "p455w0rd",
					},
				},
			},
			expectedOutput: DockerConfigJSON{
				Auths: map[string]DockerConfigEntry{
					"registry.hostname.com": DockerConfigEntry{
						Username: "user",
						Password: "p455w0rd",
					},
					"second-registry.hostname.com": DockerConfigEntry{
						Username: "user",
						Password: "p455w0rd",
					},
				},
			},
		},
		{
			name: "Second overrides first",
			input1: DockerConfigJSON{
				Auths: map[string]DockerConfigEntry{
					"registry.hostname.com": DockerConfigEntry{
						Username: "user",
						Password: "p455w0rd",
					},
				},
			},
			input2: DockerConfigJSON{
				Auths: map[string]DockerConfigEntry{
					"registry.hostname.com": DockerConfigEntry{
						Username: "otheruser",
						Password: "p455w0rd",
					},
				},
			},
			expectedOutput: DockerConfigJSON{
				Auths: map[string]DockerConfigEntry{
					"registry.hostname.com": DockerConfigEntry{
						Username: "otheruser",
						Password: "p455w0rd",
					},
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			merger := NewSecretMerger()

			assert.NoError(t, merger.Insert(testCase.input1))
			assert.NoError(t, merger.Insert(testCase.input2))

			assert.Equal(t, testCase.expectedOutput, merger.ImageRegistrySecret().DockerConfigJSON())
		})
	}
}
