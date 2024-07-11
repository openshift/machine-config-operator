package build

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateImagePullspecHasDigest(t *testing.T) {
	validPullspecs := []string{
		"registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:12e89d631c0ca1700262583acfb856b6e7dbe94800cb38035d68ee5cc912411c",
		"registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:5b6d901069e640fc53d2e971fa1f4802bf9dea1a4ffba67b8a17eaa7d8dfa336",
	}

	for _, pullspec := range validPullspecs {
		assert.NoError(t, validateImageHasDigestedPullspec(pullspec))
	}

	invalidPullspecs := []string{
		expectedImagePullspecWithTag,
	}

	for _, pullspec := range invalidPullspecs {
		assert.Error(t, validateImageHasDigestedPullspec(pullspec))
	}
}

// Tests that a given image pullspec with a tag and SHA is correctly substituted.
func TestParseImagePullspec(t *testing.T) {
	t.Parallel()

	out, err := ParseImagePullspec(expectedImagePullspecWithTag, expectedImageSHA)
	assert.NoError(t, err)
	assert.Equal(t, expectedImagePullspecWithSHA, out)
}

// Tests that pull secrets are canonicalized. In other words, converted from
// the legacy-style pull secret to the new-style secret.
func TestCanonicalizePullSecret(t *testing.T) {
	t.Parallel()

	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name            string
		inputSecret     *corev1.Secret
		expectCanonical bool
		expectError     bool
	}{
		{
			name: "new-style secret dockerconfigjson",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
			expectCanonical: false,
		},
		{
			name: "new-style secret dockercfg",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeDockercfg,
			},
			expectCanonical: false,
		},
		{
			name: "legacy secret dockercfg",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(legacySecret),
				},
				Type: corev1.SecretTypeDockercfg,
			},
			expectCanonical: true,
		},
		{
			name: "legacy secret dockerconfigjson",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(legacySecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
			expectCanonical: true,
		},
		{
			name: "empty secret",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: {},
				},
				Type: corev1.SecretTypeDockercfg,
			},
			expectError: true,
		},
		{
			name: "unknown key secret",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					"unknown-key": []byte(newSecret),
				},
			},
			expectError: true,
		},
		{
			name: "unknown secret type",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeOpaque,
			},
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out, err := canonicalizePullSecret(testCase.inputSecret)
			if testCase.expectError {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
			}

			if testCase.expectCanonical {
				assert.Contains(t, out.Name, "canonical")
				assert.True(t, isCanonicalizedSecret(out))
				assert.True(t, hasCanonicalizedSecretLabels(out))
			}

			for _, val := range out.Data {
				assert.JSONEq(t, newSecret, string(val))
			}
		})
	}
}

func TestValidateOnClusterBuildConfig(t *testing.T) {
	t.Parallel()

	newMosc := func() *mcfgv1alpha1.MachineOSConfig {
		return newMachineOSConfig(newMachineConfigPool("worker"))
	}

	testCases := []struct {
		name            string
		errExpected     bool
		secretsToDelete []string
		mosc            func() *mcfgv1alpha1.MachineOSConfig
	}{
		{
			name: "happy path",
			mosc: newMosc,
		},
		{
			name:            "missing secret",
			secretsToDelete: []string{"current-image-pull-secret"},
			mosc:            newMosc,
			errExpected:     true,
		},
		{
			name: "missing MachineOSConfig",
			mosc: func() *mcfgv1alpha1.MachineOSConfig {
				mosc := newMosc()
				mosc.Name = "other-machineosconfig"
				mosc.Spec.MachineConfigPool.Name = "other-machineconfigpool"
				return mosc
			},
			errExpected: true,
		},
		{
			name: "malformed image pullspec",
			mosc: func() *mcfgv1alpha1.MachineOSConfig {
				mosc := newMosc()
				mosc.Spec.BuildInputs.RenderedImagePushspec = "malformed-image-pullspec"
				return mosc
			},
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			clients := getClientsForTest()

			_, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Create(context.TODO(), testCase.mosc(), metav1.CreateOptions{})
			require.NoError(t, err)

			for _, secret := range testCase.secretsToDelete {
				err := clients.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret, metav1.DeleteOptions{})
				require.NoError(t, err)
			}

			err = ValidateOnClusterBuildConfig(clients.kubeclient, clients.mcfgclient, []*mcfgv1.MachineConfigPool{newMachineConfigPool("worker")})
			if testCase.errExpected {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
