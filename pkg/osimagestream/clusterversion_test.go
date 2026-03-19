// Assisted-by: Claude
package osimagestream

import (
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfigclientset "github.com/openshift/client-go/config/clientset/versioned/fake"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetClusterVersion(t *testing.T) {
	tests := []struct {
		name           string
		clusterVersion *configv1.ClusterVersion
		errorContains  string
	}{
		{
			name: "success - returns ClusterVersion",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
			},
		},
		{
			name:          "error - ClusterVersion does not exist",
			errorContains: "failed to get ClusterVersion",
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

			cv, err := GetClusterVersion(clusterVersionInformer.Lister())
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.clusterVersion, cv)
		})
	}
}

func TestGetReleasePayloadImage(t *testing.T) {
	tests := []struct {
		name           string
		clusterVersion *configv1.ClusterVersion
		expectedImage  string
		errorContains  string
	}{
		{
			name: "success - returns image from ClusterVersion",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					Desired: configv1.Release{
						Image: "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
					},
				},
			},
			expectedImage: "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
		},
		{
			name:          "error - nil ClusterVersion",
			errorContains: "ClusterVersion desired image is not yet available",
		},
		{
			name: "error - ClusterVersion has empty desired image",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					Desired: configv1.Release{Image: ""},
				},
			},
			errorContains: "ClusterVersion desired image is not yet available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image, err := GetReleasePayloadImage(tt.clusterVersion)
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

func TestGetInstallVersion(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	t1 := metav1.NewTime(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))
	t2 := metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	tests := []struct {
		name           string
		clusterVersion *configv1.ClusterVersion
		expectedMajor  uint
		expectedMinor  uint
		errorContains  string
	}{
		{
			name: "single completed entry returns that version",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					History: []configv1.UpdateHistory{
						{State: configv1.CompletedUpdate, Version: "4.16.0", CompletionTime: &t0},
					},
				},
			},
			expectedMajor: 4,
			expectedMinor: 16,
		},
		{
			name: "multiple entries returns the oldest completed",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					History: []configv1.UpdateHistory{
						{State: configv1.CompletedUpdate, Version: "5.0.0", CompletionTime: &t2},
						{State: configv1.CompletedUpdate, Version: "4.18.0", CompletionTime: &t1},
						{State: configv1.CompletedUpdate, Version: "4.16.0", CompletionTime: &t0},
					},
				},
			},
			expectedMajor: 4,
			expectedMinor: 16,
		},
		{
			name: "unsorted history returns the oldest completed by completion time",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					History: []configv1.UpdateHistory{
						{State: configv1.CompletedUpdate, Version: "4.18.0", CompletionTime: &t1},
						{State: configv1.CompletedUpdate, Version: "5.0.0", CompletionTime: &t2},
						{State: configv1.CompletedUpdate, Version: "4.16.0", CompletionTime: &t0},
					},
				},
			},
			expectedMajor: 4,
			expectedMinor: 16,
		},
		{
			name: "in-progress entries are ignored",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					History: []configv1.UpdateHistory{
						{State: configv1.PartialUpdate, Version: "5.0.0", CompletionTime: nil},
						{State: configv1.CompletedUpdate, Version: "4.18.0", CompletionTime: &t1},
						{State: configv1.CompletedUpdate, Version: "4.16.0", CompletionTime: &t0},
					},
				},
			},
			expectedMajor: 4,
			expectedMinor: 16,
		},
		{
			name: "no completed entries returns error",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					History: []configv1.UpdateHistory{
						{State: configv1.PartialUpdate, Version: "4.16.0", CompletionTime: nil},
					},
				},
			},
			errorContains: "no completed updates",
		},
		{
			name: "empty history returns error",
			clusterVersion: &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status:     configv1.ClusterVersionStatus{},
			},
			errorContains: "no completed updates",
		},
		{
			name:          "nil ClusterVersion returns error",
			errorContains: "ClusterVersion cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := GetInstallVersion(tt.clusterVersion)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedMajor, v.Major())
			assert.Equal(t, tt.expectedMinor, v.Minor())
		})
	}
}
