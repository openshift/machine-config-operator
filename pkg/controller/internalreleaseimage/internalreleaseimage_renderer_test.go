package internalreleaseimage

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// intermediateCiphers is what the Intermediate profile reduces to once the suites the
// registry cannot use at TLS 1.2 are dropped.
var intermediateCiphers = []string{
	"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
	"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
	"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
	"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
}

// TestRegistryTLSFromProfile covers the translation from each OpenShift TLS profile to
// the pair of values the registry unit renders.
func TestRegistryTLSFromProfile(t *testing.T) {
	cases := []struct {
		name            string
		profile         *configv1.TLSSecurityProfile
		expectedVersion string
		expectedCiphers []string
		expectErr       bool
	}{
		{
			// The Old profile asks for TLS 1.0, which the registry rejects outright.
			// It must be clamped rather than passed through.
			name:            "Old profile is clamped to the lowest version the registry accepts",
			profile:         &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedVersion: "tls1.2",
		},
		{
			name:            "Intermediate profile",
			profile:         &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType},
			expectedVersion: "tls1.2",
			expectedCiphers: intermediateCiphers,
		},
		{
			// Cipher suites are fixed by the protocol at TLS 1.3, and the registry
			// ignores the variable entirely above tls1.2.
			name:            "Modern profile sets no cipher suites",
			profile:         &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedVersion: "tls1.3",
			expectedCiphers: nil,
		},
		{
			name:            "unset profile falls back to Intermediate",
			profile:         nil,
			expectedVersion: "tls1.2",
			expectedCiphers: intermediateCiphers,
		},
		{
			name: "Custom profile keeps only the suites the registry accepts",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers: []string{
							"ECDHE-RSA-AES128-GCM-SHA256", // kept
							"ECDHE-RSA-AES128-SHA256",     // dropped, registry has no such suite
						},
					},
				},
			},
			expectedVersion: "tls1.2",
			expectedCiphers: []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		},
		{
			// Rendering nothing here would leave the registry on its own defaults,
			// which are broader than the profile asked for.
			name: "Custom profile with no usable suite is an error, not a silent fallback",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"DHE-RSA-AES128-GCM-SHA256"},
					},
				},
			},
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version, ciphers, err := registryTLSFromProfile(tc.profile)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expectedVersion, version)
			if tc.expectedCiphers != nil {
				assert.Equal(t, tc.expectedCiphers, ciphers)
			}
		})
	}
}

// TestRegistryTLSFromProfileEmitsOnlyAcceptedValues is the guard against the whole
// class of bug here: the registry errors out and crash-loops on any minimum version
// or cipher suite name it does not recognise, and one unknown name rejects the entire
// cipher list. So nothing that reaches the unit file may fall outside its tables.
func TestRegistryTLSFromProfileEmitsOnlyAcceptedValues(t *testing.T) {
	for _, profileType := range []configv1.TLSProfileType{
		configv1.TLSProfileOldType,
		configv1.TLSProfileIntermediateType,
		configv1.TLSProfileModernType,
	} {
		t.Run(string(profileType), func(t *testing.T) {
			version, ciphers, err := registryTLSFromProfile(&configv1.TLSSecurityProfile{Type: profileType})
			require.NoError(t, err)
			assert.True(t, registryMinTLSVersions[version], "minimum version %q would be rejected by the registry", version)
			for _, c := range ciphers {
				assert.True(t, registryCipherSuites[c], "cipher suite %q would be rejected by the registry", c)
			}
		})
	}
}

// TestOldProfileDropsSuitesTheRegistryRejects pins the specific names OpenShift's Old
// profile asks for that the registry has no entry for. If a future rebase of the
// registry adds them back, this test is the prompt to widen registryCipherSuites.
func TestOldProfileDropsSuitesTheRegistryRejects(t *testing.T) {
	_, ciphers, err := registryTLSFromProfile(&configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType})
	require.NoError(t, err)

	_, requested := ctrlcommon.GetSecurityProfileCiphers(&configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType})
	assert.Subset(t, requested, ciphers)

	for _, dropped := range []string{
		"TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",
		"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",
		"TLS_RSA_WITH_AES_128_CBC_SHA256",
	} {
		assert.Contains(t, requested, dropped, "Old profile no longer requests %s; this test needs updating", dropped)
		assert.NotContains(t, ciphers, dropped)
	}
}
