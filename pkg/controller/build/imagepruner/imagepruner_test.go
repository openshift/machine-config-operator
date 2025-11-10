package imagepruner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeImageInspector is a fake image inspector implementation used for testing
// the ImagePruner without requiring an actual image registry.
type fakeImageInspector struct {
	imageInspectCalled   bool
	imageInspectPullspec string
	imageInspectSysCtx   *types.SystemContext
	deleteImageCalled    bool
	deleteImagePullspec  string
	deleteImageSysCtx    *types.SystemContext
	imageApiError        bool
}

// ImageInspect is a mock implementation for testing, setting flags to indicate it was called.
func (f *fakeImageInspector) ImageInspect(_ context.Context, sysCtx *types.SystemContext, pullspec string) (*types.ImageInspectInfo, *digest.Digest, error) {
	f.imageInspectCalled = true
	f.imageInspectPullspec = pullspec
	if f.imageApiError {
		return nil, nil, fmt.Errorf("fake inspect error")
	}
	return nil, nil, nil
}

// DeleteImage is a mock implementation for testing, setting flags to indicate it was called.
func (f *fakeImageInspector) DeleteImage(_ context.Context, sysCtx *types.SystemContext, pullspec string) error {
	f.deleteImageCalled = true
	f.deleteImagePullspec = pullspec
	if f.imageApiError {
		return fmt.Errorf("fake delete error")
	}
	return nil
}

// TestImagePruner is a unit test that validates the ImagePruner's setup and teardown
// of temporary directories for authfiles and certificates. It tests both
// DockerConfigJSON and Dockercfg secret types.
func TestImagePruner(t *testing.T) {
	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name        string
		inputSecret *corev1.Secret
		authError   bool
		imageError  bool
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
		{
			name: "ImageErrorHandling",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
			imageError: true,
		},
		{
			name: "AuthErrorHandling",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeOpaque,
			},
			authError: true,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fii := &fakeImageInspector{
				imageApiError: testCase.imageError,
			}
			ip := &imagePrunerImpl{
				images: fii,
			}

			pullspec := "registry.hostname.com/org/repo:latest"

			cc := &mcfgv1.ControllerConfig{}
			_, _, err := ip.InspectImage(ctx, pullspec, testCase.inputSecret, cc)
			if testCase.imageError {
				assert.Error(t, err)
				// SysContext context created but the API call failed
				assert.True(t, fii.imageInspectCalled)
			} else if testCase.authError {
				assert.Error(t, err)
				// The API called wasn't performed cause the logic couldn't reach that point
				assert.False(t, fii.deleteImageCalled)
			} else {
				assert.NoError(t, err)
				assert.True(t, fii.imageInspectCalled)
				assert.Equal(t, fii.imageInspectPullspec, pullspec)
			}

			err = ip.DeleteImage(ctx, pullspec, testCase.inputSecret, cc)
			if testCase.imageError {
				assert.Error(t, err)
				// SysContext context created but the API call failed
				assert.True(t, fii.deleteImageCalled)
			} else if testCase.authError {
				assert.Error(t, err)
				// The API called wasn't performed cause the logic couldn't reach that point
				assert.False(t, fii.deleteImageCalled)
			} else {
				assert.NoError(t, err)
				assert.True(t, fii.deleteImageCalled)
				assert.Equal(t, fii.deleteImagePullspec, pullspec)
			}
		})
	}
}
