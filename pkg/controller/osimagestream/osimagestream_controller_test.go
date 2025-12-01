// Assisted-by: Claude
package osimagestream

import (
	"context"
	"testing"
	"time"

	"github.com/containers/image/v5/types"
	configv1 "github.com/openshift/api/config/v1"
	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	configfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	mcfgfake "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeinformers "k8s.io/client-go/informers"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// mockImageStreamFactory is a test implementation of ImageStreamFactory
type mockImageStreamFactory struct {
	runtimeStream   *v1alpha1.OSImageStream
	runtimeErr      error
	bootstrapStream *v1alpha1.OSImageStream
	bootstrapErr    error
}

func (m *mockImageStreamFactory) CreateRuntimeSources(_ context.Context, _ string, _ *types.SystemContext) (*v1alpha1.OSImageStream, error) {
	return m.runtimeStream, m.runtimeErr
}

func (m *mockImageStreamFactory) CreateBootstrapSources(_ context.Context, _ *imagev1.ImageStream, _ *OSImageTuple, _ *types.SystemContext) (*v1alpha1.OSImageStream, error) {
	return m.bootstrapStream, m.bootstrapErr
}

func TestController_Run_BootSuccess(t *testing.T) {
	// Create existing OSImageStream with current version (no update needed)
	existingOSImageStream := &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInstanceNameOSImageStream,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          version.Hash,
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			},
		},
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
			},
		},
	}

	// Create fake clients
	mcfgObjs := []runtime.Object{existingOSImageStream}
	fakeMcfgClient := mcfgfake.NewSimpleClientset(mcfgObjs...)
	mcfgInformerFactory := mcfginformers.NewSharedInformerFactory(fakeMcfgClient, 0)

	configObjs := []runtime.Object{}
	fakeConfigClient := configfake.NewSimpleClientset(configObjs...)
	configInformerFactory := configinformers.NewSharedInformerFactory(fakeConfigClient, 0)

	fakeKubeClient := kubefake.NewSimpleClientset()
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(fakeKubeClient, 0)

	// Setup informers
	ccInformer := mcfgInformerFactory.Machineconfiguration().V1().ControllerConfigs()
	cmInformer := kubeInformerFactory.Core().V1().ConfigMaps()
	osImageStreamInformer := mcfgInformerFactory.Machineconfiguration().V1alpha1().OSImageStreams()
	cvInformer := configInformerFactory.Config().V1().ClusterVersions()

	// Add objects to indexers
	osImageStreamInformer.Informer().GetIndexer().Add(existingOSImageStream)

	// Mock factory (not used since no update is needed)
	mockFactory := &mockImageStreamFactory{}

	// Create controller using constructor
	ctrl := NewController(
		fakeKubeClient,
		fakeMcfgClient,
		ccInformer,
		cmInformer,
		osImageStreamInformer,
		cvInformer,
		mockFactory,
	)

	// Start informers
	stopCh := make(chan struct{})
	defer close(stopCh)

	mcfgInformerFactory.Start(stopCh)
	configInformerFactory.Start(stopCh)
	kubeInformerFactory.Start(stopCh)

	// Run controller in goroutine
	go ctrl.Run(stopCh)

	// Wait for boot to complete using WaitBoot
	done := make(chan error, 1)
	go func() {
		done <- ctrl.WaitBoot()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)

		// Verify the OSImageStream was not modified (remains as it was)
		osImageStream, err := fakeMcfgClient.MachineconfigurationV1alpha1().
			OSImageStreams().
			Get(context.TODO(), ctrlcommon.ClusterInstanceNameOSImageStream, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, existingOSImageStream, osImageStream)
	case <-time.After(2 * time.Second):
		t.Fatal("Boot did not complete in time")
	}
}

func TestController_Run_NoOSImageStream(t *testing.T) {
	// No existing OSImageStream - controller should create one

	// New OSImageStream that will be returned by the mock factory and created
	newOSImageStream := &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInstanceNameOSImageStream,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          version.Hash,
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			},
		},
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
			},
		},
	}

	// Create fake clients
	mcfgObjs := []runtime.Object{}
	fakeMcfgClient := mcfgfake.NewSimpleClientset(mcfgObjs...)
	mcfgInformerFactory := mcfginformers.NewSharedInformerFactory(fakeMcfgClient, 0)

	// Provide ClusterVersion
	clusterVersion := &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
		Status: configv1.ClusterVersionStatus{
			Desired: configv1.Release{
				Image: "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
			},
		},
	}
	configObjs := []runtime.Object{clusterVersion}
	fakeConfigClient := configfake.NewSimpleClientset(configObjs...)
	configInformerFactory := configinformers.NewSharedInformerFactory(fakeConfigClient, 0)

	// Provide ControllerConfig with PullSecret
	controllerConfig := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ControllerConfigName,
		},
		Spec: mcfgv1.ControllerConfigSpec{
			PullSecret: &corev1.ObjectReference{
				Name:      "test-pull-secret",
				Namespace: "openshift-config",
			},
		},
	}
	mcfgObjs = append(mcfgObjs, controllerConfig)
	fakeMcfgClient = mcfgfake.NewSimpleClientset(mcfgObjs...)
	mcfgInformerFactory = mcfginformers.NewSharedInformerFactory(fakeMcfgClient, 0)

	// Provide PullSecret
	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pull-secret",
			Namespace: "openshift-config",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`),
		},
	}
	fakeKubeClient := kubefake.NewSimpleClientset(pullSecret)
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(fakeKubeClient, 0)

	// Setup informers
	ccInformer := mcfgInformerFactory.Machineconfiguration().V1().ControllerConfigs()
	cmInformer := kubeInformerFactory.Core().V1().ConfigMaps()
	osImageStreamInformer := mcfgInformerFactory.Machineconfiguration().V1alpha1().OSImageStreams()
	cvInformer := configInformerFactory.Config().V1().ClusterVersions()

	// Add objects to indexers
	cvInformer.Informer().GetIndexer().Add(clusterVersion)
	ccInformer.Informer().GetIndexer().Add(controllerConfig)

	// Mock factory that returns the new OSImageStream
	mockFactory := &mockImageStreamFactory{
		runtimeStream: newOSImageStream,
	}

	// Create controller using constructor
	ctrl := NewController(
		fakeKubeClient,
		fakeMcfgClient,
		ccInformer,
		cmInformer,
		osImageStreamInformer,
		cvInformer,
		mockFactory,
	)

	// Start informers
	stopCh := make(chan struct{})
	defer close(stopCh)

	mcfgInformerFactory.Start(stopCh)
	configInformerFactory.Start(stopCh)
	kubeInformerFactory.Start(stopCh)

	// Run controller in goroutine
	go ctrl.Run(stopCh)

	// Wait for boot to complete using WaitBoot
	done := make(chan error, 1)
	go func() {
		done <- ctrl.WaitBoot()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)

		// Verify the OSImageStream was created
		created, err := fakeMcfgClient.MachineconfigurationV1alpha1().
			OSImageStreams().
			Get(context.TODO(), ctrlcommon.ClusterInstanceNameOSImageStream, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, version.Hash, created.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey])
		assert.Equal(t, "rhel-9", created.Status.DefaultStream)
		assert.Len(t, created.Status.AvailableStreams, 1)
		assert.Equal(t, v1alpha1.ImageDigestFormat("image1"), created.Status.AvailableStreams[0].OSImage)
	case <-time.After(2 * time.Second):
		t.Fatal("Boot did not complete in time")
	}
}

func TestController_Run_OldVersion(t *testing.T) {
	// Create existing OSImageStream with old version (update needed)
	existingOSImageStream := &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInstanceNameOSImageStream,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey: "old-version-hash",
			},
		},
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9-old",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9-old", OSImage: "old-image", OSExtensionsImage: "old-ext"},
			},
		},
	}

	// Updated OSImageStream that will be returned by the mock factory
	updatedOSImageStream := &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInstanceNameOSImageStream,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          version.Hash,
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			},
		},
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "new-image", OSExtensionsImage: "new-ext"},
			},
		},
	}

	// Create fake clients
	mcfgObjs := []runtime.Object{existingOSImageStream}
	fakeMcfgClient := mcfgfake.NewSimpleClientset(mcfgObjs...)
	mcfgInformerFactory := mcfginformers.NewSharedInformerFactory(fakeMcfgClient, 0)

	// Provide ClusterVersion
	clusterVersion := &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
		Status: configv1.ClusterVersionStatus{
			Desired: configv1.Release{
				Image: "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
			},
		},
	}
	configObjs := []runtime.Object{clusterVersion}
	fakeConfigClient := configfake.NewSimpleClientset(configObjs...)
	configInformerFactory := configinformers.NewSharedInformerFactory(fakeConfigClient, 0)

	// Provide ControllerConfig with PullSecret
	controllerConfig := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ControllerConfigName,
		},
		Spec: mcfgv1.ControllerConfigSpec{
			PullSecret: &corev1.ObjectReference{
				Name:      "test-pull-secret",
				Namespace: "openshift-config",
			},
		},
	}
	mcfgObjs = append(mcfgObjs, controllerConfig)
	fakeMcfgClient = mcfgfake.NewSimpleClientset(mcfgObjs...)
	mcfgInformerFactory = mcfginformers.NewSharedInformerFactory(fakeMcfgClient, 0)

	// Provide PullSecret
	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pull-secret",
			Namespace: "openshift-config",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`),
		},
	}
	fakeKubeClient := kubefake.NewSimpleClientset(pullSecret)
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(fakeKubeClient, 0)

	// Setup informers
	ccInformer := mcfgInformerFactory.Machineconfiguration().V1().ControllerConfigs()
	cmInformer := kubeInformerFactory.Core().V1().ConfigMaps()
	osImageStreamInformer := mcfgInformerFactory.Machineconfiguration().V1alpha1().OSImageStreams()
	cvInformer := configInformerFactory.Config().V1().ClusterVersions()

	// Add objects to indexers
	osImageStreamInformer.Informer().GetIndexer().Add(existingOSImageStream)
	cvInformer.Informer().GetIndexer().Add(clusterVersion)
	ccInformer.Informer().GetIndexer().Add(controllerConfig)

	// Mock factory that returns the updated OSImageStream
	mockFactory := &mockImageStreamFactory{
		runtimeStream: updatedOSImageStream,
	}

	// Create controller using constructor
	ctrl := NewController(
		fakeKubeClient,
		fakeMcfgClient,
		ccInformer,
		cmInformer,
		osImageStreamInformer,
		cvInformer,
		mockFactory,
	)

	// Start informers
	stopCh := make(chan struct{})
	defer close(stopCh)

	mcfgInformerFactory.Start(stopCh)
	configInformerFactory.Start(stopCh)
	kubeInformerFactory.Start(stopCh)

	// Run controller in goroutine
	go ctrl.Run(stopCh)

	// Wait for boot to complete using WaitBoot
	done := make(chan error, 1)
	go func() {
		done <- ctrl.WaitBoot()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)

		// Verify the OSImageStream was updated
		updated, err := fakeMcfgClient.MachineconfigurationV1alpha1().
			OSImageStreams().
			Get(context.TODO(), ctrlcommon.ClusterInstanceNameOSImageStream, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, version.Hash, updated.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey])
		assert.Equal(t, "rhel-9", updated.Status.DefaultStream)
		assert.Equal(t, v1alpha1.ImageDigestFormat("new-image"), updated.Status.AvailableStreams[0].OSImage)
	case <-time.After(2 * time.Second):
		t.Fatal("Boot did not complete in time")
	}
}
