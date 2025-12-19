// Assisted-by: Claude
package osimagestream

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfigclientset "github.com/openshift/client-go/config/clientset/versioned/fake"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetClusterVersionOperatorImage(t *testing.T) {
	tests := []struct {
		name           string
		clusterVersion *configv1.ClusterVersion
		expectedImage  string
		errorContains  string
	}{
		{
			name: "success - returns image from ClusterVersion",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name: "version",
				},
				Status: configv1.ClusterVersionStatus{
					Desired: configv1.Release{
						Image: "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
					},
				},
			},
			expectedImage: "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
		},
		{
			name:          "error - ClusterVersion does not exist",
			errorContains: "not found",
		},
		{
			name: "error - ClusterVersion has empty desired image",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name: "version",
				},
				Status: configv1.ClusterVersionStatus{
					Desired: configv1.Release{
						Image: "",
					},
				},
			},
			errorContains: "ClusterVersion desired image is not yet available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fakeClient *fakeconfigclientset.Clientset
			if tt.clusterVersion != nil {
				fakeClient = fakeconfigclientset.NewSimpleClientset(tt.clusterVersion)
			} else {
				fakeClient = fakeconfigclientset.NewSimpleClientset()
			}

			informerFactory := configinformers.NewSharedInformerFactory(fakeClient, 0)
			clusterVersionInformer := informerFactory.Config().V1().ClusterVersions()

			if tt.clusterVersion != nil {
				err := clusterVersionInformer.Informer().GetIndexer().Add(tt.clusterVersion)
				require.NoError(t, err)
			}

			image, err := GetReleasePayloadImage(clusterVersionInformer.Lister())

			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Empty(t, image)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedImage, image)
		})
	}
}
