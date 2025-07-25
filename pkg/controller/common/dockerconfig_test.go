package common

import (
	"testing"

	"github.com/openshift/machine-config-operator/pkg/secrets"
	"github.com/stretchr/testify/assert"
)

func TestConvertSecretTodockercfg(t *testing.T) {
	t.Parallel()

	dockerconfigjson := []byte(`{"auths":{"registry.hostname.com":{"username":"user","password":"p455w0rd"}}}`)
	dockercfg := []byte(`{"registry.hostname.com":{"username":"user","password":"p455w0rd"}}`)

	testCases := []struct {
		name          string
		inputBytes    []byte
		expectedBytes []byte
	}{
		{
			name:          "converts current-style secrets",
			inputBytes:    dockerconfigjson,
			expectedBytes: dockercfg,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out, err := ConvertSecretTodockercfg(testCase.inputBytes)
			assert.NoError(t, err)
			assert.Equal(t, testCase.expectedBytes, out)
		})
	}
}

func TestConvertSecretToDockerconfigJSON(t *testing.T) {
	t.Parallel()

	dockerconfigjson := []byte(`{"auths":{"registry.hostname.com":{"username":"user","password":"p455w0rd"}}}`)
	dockercfg := []byte(`{"registry.hostname.com":{"username":"user","password":"p455w0rd"}}`)

	testCases := []struct {
		name              string
		inputBytes        []byte
		expectedBytes     []byte
		expectedConverted bool
	}{
		{
			name:              "converts legacy-style secrets",
			inputBytes:        dockercfg,
			expectedBytes:     dockerconfigjson,
			expectedConverted: true,
		},
		{
			name:          "skips current-style secrets",
			inputBytes:    dockerconfigjson,
			expectedBytes: dockerconfigjson,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out, converted, err := ConvertSecretToDockerconfigJSON(testCase.inputBytes)
			assert.NoError(t, err)
			assert.Equal(t, testCase.expectedBytes, out)
			assert.Equal(t, testCase.expectedConverted, converted)
		})
	}
}

func TestToDockerConfigJSON(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		inputBytes []byte
	}{
		{
			name:       "converts from new-style config",
			inputBytes: []byte(`{"auths":{"registry.hostname.com":{"password":"p455w0rd","username":"user"}}}`),
		},
		{
			name:       "converts from legacy-style config",
			inputBytes: []byte(`{"registry.hostname.com":{"password":"p455w0rd","username":"user"}}`),
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			expected := &secrets.DockerConfigJSON{
				Auths: secrets.DockerConfig{
					"registry.hostname.com": {
						Username: "user",
						Password: "p455w0rd",
					},
				},
			}

			out, err := ToDockerConfigJSON(testCase.inputBytes)
			assert.NoError(t, err)
			assert.Equal(t, expected, out)
		})
	}
}

func TestMergeDockerConfigstoJSONMap(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		inputBytes      []byte
		inputEntries    map[string]secrets.DockerConfigEntry
		expectedEntries map[string]secrets.DockerConfigEntry
	}{
		{
			name:       "simple concatenation",
			inputBytes: []byte(`{"registry.hostname.com":{"password":"p455w0rd","username":"user"}}`),
			inputEntries: map[string]secrets.DockerConfigEntry{
				"second-registry.hostname.com": {
					Username: "user",
					Password: "p455w0rd",
				},
			},
			expectedEntries: map[string]secrets.DockerConfigEntry{
				"registry.hostname.com": {
					Username: "user",
					Password: "p455w0rd",
				},
				"second-registry.hostname.com": {
					Username: "user",
					Password: "p455w0rd",
				},
			},
		},
		{
			name:       "JSON overrides entry",
			inputBytes: []byte(`{"registry.hostname.com":{"password":"p455w0rd","username":"other-user"}}`),
			inputEntries: map[string]secrets.DockerConfigEntry{
				"registry.hostname.com": {
					Username: "user",
					Password: "p455w0rd",
				},
			},
			expectedEntries: map[string]secrets.DockerConfigEntry{
				"registry.hostname.com": {
					Username: "other-user",
					Password: "p455w0rd",
				},
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.NoError(t, MergeDockerConfigstoJSONMap(testCase.inputBytes, testCase.inputEntries))
			assert.Equal(t, testCase.expectedEntries, testCase.inputEntries)
		})
	}
}
