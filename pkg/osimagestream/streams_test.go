package osimagestream

import (
	"testing"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sversion "k8s.io/apimachinery/pkg/util/version"
)

// TestGetBuiltinDefaultStreamName verifies that the builtin default stream name
// is resolved correctly for both OCP (RHEL) and OKD (SCOS) builds.
// This test mutates and restores package-level globals (version.SCOS,
// version.ReleaseVersion) and must NOT be run in parallel.
func TestGetBuiltinDefaultStreamName(t *testing.T) {
	tests := []struct {
		name           string
		scos           bool
		installVersion string
		releaseVersion string
		expected       string
		expectErr      bool
	}{
		{
			name:           "SCOS always returns centos-10",
			scos:           true,
			installVersion: "5.0.0",
			expected:       StreamNameCentOS10,
		},
		{
			name:           "SCOS returns centos-10 even for 4.x",
			scos:           true,
			installVersion: "4.18.0",
			expected:       StreamNameCentOS10,
		},
		{
			name:     "SCOS returns centos-10 with nil installVersion",
			scos:     true,
			expected: StreamNameCentOS10,
		},
		{
			name:           "OCP 4.x returns rhel-9",
			installVersion: "4.18.0",
			expected:       StreamNameRHEL9,
		},
		{
			name:           "OCP 5.x returns rhel-10",
			installVersion: "5.0.0",
			expected:       StreamNameRHEL10,
		},
		{
			name:           "OCP falls back to releaseVersion when installVersion is nil",
			releaseVersion: "4.19.0",
			expected:       StreamNameRHEL9,
		},
		{
			name:           "OCP falls back to releaseVersion 5.x",
			releaseVersion: "5.1.0",
			expected:       StreamNameRHEL10,
		},
		{
			name:           "OCP errors on unparseable releaseVersion",
			releaseVersion: "not-a-version",
			expectErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origSCOS := version.SCOS
			origRelease := version.ReleaseVersion
			t.Cleanup(func() {
				version.SCOS = origSCOS
				version.ReleaseVersion = origRelease
			})

			version.SCOS = tt.scos
			if tt.releaseVersion != "" {
				version.ReleaseVersion = tt.releaseVersion
			}

			var installVersion *k8sversion.Version
			if tt.installVersion != "" {
				installVersion = k8sversion.MustParseGeneric(tt.installVersion)
			}

			result, err := GetBuiltinDefaultStreamName(installVersion)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
