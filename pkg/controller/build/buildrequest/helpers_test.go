package buildrequest

import (
	"testing"

	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
		"registry.ci.openshift.org/org/repo:latest",
	}

	for _, pullspec := range invalidPullspecs {
		assert.Error(t, validateImageHasDigestedPullspec(pullspec))
	}
}

// Tests that pull secrets are canonicalized. Since the code that handles the
// legacy secret conversion is no longer in this package, this test will only
// concern itself with whether the output has the appropriate labels and other
// settings that we set within this module.
func TestCanonicalizePullSecret(t *testing.T) {
	t.Parallel()

	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name   string
		secret *corev1.Secret
	}{
		{
			name: "DockerConfigJSON",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
		},
		{
			name: "Dockercfg",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(legacySecret),
				},
				Type: corev1.SecretTypeDockercfg,
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			out, err := canonicalizePullSecret(testCase.secret)

			assert.NoError(t, err)
			assert.Contains(t, out.Name, "canonical")
			assert.True(t, utils.CanonicalizedSecretSelector().Matches(labels.Set(out.GetLabels())))
			assert.True(t, utils.IsObjectCreatedByBuildController(out))
		})
	}
}
