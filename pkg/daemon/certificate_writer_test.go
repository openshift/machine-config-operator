package daemon

import (
	"os"
	"path/filepath"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestImagePullSecretReconciliation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                        string
		nodeRoles                   []string
		writeMountedSecretBytes     bool
		mountedSecretBytes          []byte
		controllerConfigSecretBytes []byte
		expectedBytes               []byte
		errExpected                 bool
		unlayeredNode               bool
	}{
		// Tests that secrets from both the mounted secrets and ControllerConfig
		// can be merged together without issue.
		{
			name:                        "simple concatenation",
			nodeRoles:                   []string{"node-role.kubernetes.io/worker"},
			writeMountedSecretBytes:     true,
			mountedSecretBytes:          []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			controllerConfigSecretBytes: []byte(`{"auths": {"other-registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			// TODO(zzlotnik): Can we omit the unused fields?
			expectedBytes: []byte(`{"auths":{"other-registry.hostname.com":{"username":"user","password":"secret","email":"","auth":""},"registry.hostname.com":{"username":"user","password":"secret","email":"","auth":""}}}`),
		},
		// Verifies that secrets from the mounted secret path take precedence over
		// the ones from the ControllerConfig.
		{
			name:                        "mounted secret takes precedence",
			nodeRoles:                   []string{"node-role.kubernetes.io/worker"},
			writeMountedSecretBytes:     true,
			mountedSecretBytes:          []byte(`{"auths": {"registry.hostname.com": {"username": "mounted-secret-user", "password": "mounted-secret-secret"}}}`),
			controllerConfigSecretBytes: []byte(`{"auths": {"registry.hostname.com": {"username": "other-user", "password": "other-secret"}}}`),
			expectedBytes:               []byte(`{"auths":{"registry.hostname.com":{"username":"mounted-secret-user","password":"mounted-secret-secret","email":"","auth":""}}}`),
		},
		// Verifies that in the absence of mounted secrets, only the ones found on
		// the ControllerConfig will be used.
		{
			name:                        "no mounted secrets",
			nodeRoles:                   []string{"node-role.kubernetes.io/worker"},
			controllerConfigSecretBytes: []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			expectedBytes:               []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "secret"}}}`),
		},
		// Verifies that if multiple node roles are found on a node (e.g., for
		// control-plane nodes) and only one of them has a mounted secret, that it
		// will be found.
		{
			name:                        "multiple node roles",
			nodeRoles:                   []string{"node-role.kubernetes.io/master", "node-role.kubernetes.io/control-plane"},
			writeMountedSecretBytes:     true,
			mountedSecretBytes:          []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			controllerConfigSecretBytes: []byte(`{"auths": {"other-registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			// TODO(zzlotnik): Can we omit the unused fields?
			expectedBytes: []byte(`{"auths":{"other-registry.hostname.com":{"username":"user","password":"secret","email":"","auth":""},"registry.hostname.com":{"username":"user","password":"secret","email":"","auth":""}}}`),
		},
		// Verifies that legacy-style secrets (that is, secrets without a top-level
		// {"auths": ...} key) are converted to a new-style secret and that any
		// other secrets are correctly merged with them.
		{
			name:                        "converts and merges legacy-style secrets",
			nodeRoles:                   []string{"node-role.kubernetes.io/worker"},
			writeMountedSecretBytes:     true,
			mountedSecretBytes:          []byte(`{"registry.hostname.com": {"username": "user", "password": "secret"}}`),
			controllerConfigSecretBytes: []byte(`{"auths":{"other-registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			// TODO(zzlotnik): Can we omit the unused fields?
			expectedBytes: []byte(`{"auths":{"other-registry.hostname.com":{"username":"user","password":"secret","email":"","auth":""},"registry.hostname.com":{"username":"user","password":"secret","email":"","auth":""}}}`),
		},
		// Verifies that we get an error when a node does not have any node roles.
		{
			name:        "zero node roles",
			nodeRoles:   []string{},
			errExpected: true,
		},
		// Verifies that we get an error when mounted secrets are composed of
		// invalid JSON. We don't have to worry about a similar case from
		// ControllerConfig since by the time we've reached this point, the image
		// pull secrets contained therein have already been parsed and validated.
		{
			name:                        "invalid JSON for mounted secrets",
			nodeRoles:                   []string{"node-role.kubernetes.io/worker"},
			writeMountedSecretBytes:     true,
			mountedSecretBytes:          []byte(`<invalid JSON>`),
			controllerConfigSecretBytes: []byte(`{"auths":{"other-registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			errExpected:                 true,
		},
		// Tests that if a node is unlayered (does not have the desired image
		// annotation), it ignores any MachineOSConfig secrets that are mounted
		// into it.
		{
			name:                        "unlayered node",
			nodeRoles:                   []string{"node-role.kubernetes.io/worker"},
			writeMountedSecretBytes:     true,
			mountedSecretBytes:          []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			controllerConfigSecretBytes: []byte(`{"auths": {"other-registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			expectedBytes:               []byte(`{"auths": {"other-registry.hostname.com": {"username": "user", "password": "secret"}}}`),
			unlayeredNode:               true,
		},
	}

	imagePullSecretKeys := []string{
		corev1.DockerConfigJsonKey,
		corev1.DockerConfigKey,
	}

	for _, testCase := range testCases {
		testCase := testCase

		for _, imagePullSecretKey := range imagePullSecretKeys {
			imagePullSecretKey := imagePullSecretKey
			t.Run(imagePullSecretKey, func(t *testing.T) {
				t.Parallel()

				t.Run(testCase.name, func(t *testing.T) {
					t.Parallel()

					node := &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{},
						},
					}

					for _, nodeRole := range testCase.nodeRoles {
						node.Labels[nodeRole] = ""
					}

					if !testCase.unlayeredNode {
						node.Annotations = map[string]string{
							constants.DesiredImageAnnotationKey: "",
						}
					}

					ctrlCfg := &mcfgv1.ControllerConfig{
						Spec: mcfgv1.ControllerConfigSpec{
							InternalRegistryPullSecret: testCase.controllerConfigSecretBytes,
						},
					}

					tmpDir := t.TempDir()

					if testCase.writeMountedSecretBytes {
						nodeRoles := getNodeRoles(node)
						nodeRole := ""
						if len(nodeRoles) == 1 {
							nodeRole = nodeRoles[0]
						} else {
							// Get the last node role instead of the first.
							nodeRole = nodeRoles[len(nodeRoles)-1]
						}

						secretDir := filepath.Join(tmpDir, osImagePullSecretDir, nodeRole)
						secretPath := filepath.Join(secretDir, imagePullSecretKey)

						require.NoError(t, os.MkdirAll(secretDir, 0o755))
						require.NoError(t, os.WriteFile(secretPath, testCase.mountedSecretBytes, 0o755))
					}

					result, err := reconcileOSImageRegistryPullSecretData(node, ctrlCfg, tmpDir)

					if testCase.errExpected {
						assert.Error(t, err)
					} else {
						assert.NoError(t, err)
					}

					assert.Equal(t, testCase.expectedBytes, result)
				})
			})
		}
	}
}
