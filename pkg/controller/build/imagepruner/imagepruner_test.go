package imagepruner

import (
	"context"
	"testing"
	"time"

	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Fake image inspector implementation so that the ImagePruner can be tested
// without requiring an image registry.
type fakeImageInspector struct {
	imageInspectCalled   bool
	imageInspectPullspec string
	imageInspectSysCtx   *types.SystemContext
	deleteImageCalled    bool
	deleteImagePullspec  string
	deleteImageSysCtx    *types.SystemContext
}

func (f *fakeImageInspector) ImageInspect(_ context.Context, sysCtx *types.SystemContext, pullspec string) (*types.ImageInspectInfo, *digest.Digest, error) {
	f.imageInspectCalled = true
	f.imageInspectPullspec = pullspec
	return nil, nil, nil
}

func (f *fakeImageInspector) DeleteImage(_ context.Context, sysCtx *types.SystemContext, pullspec string) error {
	f.deleteImageCalled = true
	f.deleteImagePullspec = pullspec
	return nil
}

// This is a unit test that validates that the imagepruner directories are set
// up and then torn down at the end.
func TestImagePruner(t *testing.T) {
	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name        string
		inputSecret *corev1.Secret
		expectError bool
	}{
		{
			name: "DockerConfigJSON",
			inputSecret: &corev1.Secret{
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
			inputSecret: &corev1.Secret{
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fii := &fakeImageInspector{}
			ip := &imagePrunerImpl{
				images: fii,
			}

			pullspec := "registry.hostname.com/org/repo:latest"

			cc := &mcfgv1.ControllerConfig{}

			sysCtx, err := ip.prepareSystemContext(testCase.inputSecret, cc)
			if testCase.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Contains(t, sysCtx.AuthFilePath, "authfile.json")
				assert.FileExists(t, sysCtx.AuthFilePath)
				assert.DirExists(t, sysCtx.DockerPerHostCertDirPath)
				assert.NoError(t, ip.cleanup(sysCtx))
				assert.NoFileExists(t, sysCtx.AuthFilePath)
				assert.NoDirExists(t, sysCtx.DockerPerHostCertDirPath)
			}

			_, _, err = ip.InspectImage(ctx, pullspec, testCase.inputSecret, cc)
			if testCase.expectError {
				assert.Error(t, err)
				assert.False(t, fii.imageInspectCalled)
			} else {
				assert.NoError(t, err)
				assert.True(t, fii.imageInspectCalled)
			}

			err = ip.DeleteImage(ctx, pullspec, testCase.inputSecret, cc)
			if testCase.expectError {
				assert.Error(t, err)
				assert.False(t, fii.deleteImageCalled)
			} else {
				assert.NoError(t, err)
				assert.True(t, fii.deleteImageCalled)
			}
		})
	}
}
