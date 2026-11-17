package bootimage

import (
	"testing"

	"github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

func newTestControllerConfig(name string, bundle []byte) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mcfgv1.ControllerConfigSpec{AdditionalTrustBundle: bundle},
	}
}

func TestGetAdditionalTrustBundle(t *testing.T) {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	ctrl := &Controller{ccLister: mcfglistersv1.NewControllerConfigLister(indexer)}

	// A missing ControllerConfig keeps the default trust roots.
	bundle, err := ctrl.getAdditionalTrustBundle()
	require.NoError(t, err)
	assert.Empty(t, bundle)

	require.NoError(t, indexer.Add(newTestControllerConfig(ctrlcommon.ControllerConfigName, []byte("old-ca"))))
	bundle, err = ctrl.getAdditionalTrustBundle()
	require.NoError(t, err)
	assert.Equal(t, []byte("old-ca"), bundle)

	// A rotated bundle is returned on the next call without restarting the controller.
	require.NoError(t, indexer.Update(newTestControllerConfig(ctrlcommon.ControllerConfigName, []byte("new-ca"))))
	bundle, err = ctrl.getAdditionalTrustBundle()
	require.NoError(t, err)
	assert.Equal(t, []byte("new-ca"), bundle)
}

func TestUpdateControllerConfigEnqueuesOnTrustBundleChange(t *testing.T) {
	name := ctrlcommon.ControllerConfigName

	tests := []struct {
		name          string
		oldObj        interface{}
		newObj        interface{}
		expectEnqueue bool
	}{
		{
			name:          "trust bundle rotated",
			oldObj:        newTestControllerConfig(name, []byte("old-ca")),
			newObj:        newTestControllerConfig(name, []byte("new-ca")),
			expectEnqueue: true,
		},
		{
			name:          "trust bundle added",
			oldObj:        newTestControllerConfig(name, nil),
			newObj:        newTestControllerConfig(name, []byte("new-ca")),
			expectEnqueue: true,
		},
		{
			name:          "trust bundle removed",
			oldObj:        newTestControllerConfig(name, []byte("old-ca")),
			newObj:        newTestControllerConfig(name, nil),
			expectEnqueue: true,
		},
		{
			name:          "trust bundle unchanged",
			oldObj:        newTestControllerConfig(name, []byte("same-ca")),
			newObj:        newTestControllerConfig(name, []byte("same-ca")),
			expectEnqueue: false,
		},
		{
			name:          "other ControllerConfig is ignored",
			oldObj:        newTestControllerConfig("other", []byte("old-ca")),
			newObj:        newTestControllerConfig("other", []byte("new-ca")),
			expectEnqueue: false,
		},
		{
			name:          "unexpected object type is ignored",
			oldObj:        &corev1.ConfigMap{},
			newObj:        &corev1.ConfigMap{},
			expectEnqueue: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &Controller{queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())}
			t.Cleanup(ctrl.queue.ShutDown)

			ctrl.updateControllerConfig(tc.oldObj, tc.newObj)
			assert.Equal(t, tc.expectEnqueue, ctrl.queue.Len() == 1)
		})
	}
}

// TestWithAdditionalTrustBundleForwardsArguments verifies that the vSphere reconcile
// adapter passes the trust bundle, and all other arguments and results, through.
func TestWithAdditionalTrustBundleForwardsArguments(t *testing.T) {
	bundle := []byte("ca-bundle")
	streamData := &stream.Stream{}
	infra := &osconfigv1.Infrastructure{}
	providerSpec := &machinev1beta1.VSphereMachineProviderSpec{}
	kubeClient := fake.NewClientset()
	newProviderSpec := &machinev1beta1.VSphereMachineProviderSpec{Template: "new-template"}

	called := false
	reconcile := withAdditionalTrustBundle(bundle, func(gotStream *stream.Stream, gotArch string, gotInfra *osconfigv1.Infrastructure, gotProviderSpec *machinev1beta1.VSphereMachineProviderSpec, gotName string, gotClient clientset.Interface, gotBundle []byte) (bool, bool, *machinev1beta1.VSphereMachineProviderSpec, string, error) {
		called = true
		assert.Same(t, streamData, gotStream)
		assert.Equal(t, "x86_64", gotArch)
		assert.Same(t, infra, gotInfra)
		assert.Same(t, providerSpec, gotProviderSpec)
		assert.Equal(t, "test-machineset", gotName)
		assert.Equal(t, clientset.Interface(kubeClient), gotClient)
		assert.Equal(t, bundle, gotBundle)
		return true, false, newProviderSpec, "", nil
	})

	patchRequired, reconcileSkipped, gotProviderSpec, rhcosVersion, err := reconcile(streamData, "x86_64", infra, providerSpec, "test-machineset", kubeClient)
	require.NoError(t, err)
	require.True(t, called)
	assert.True(t, patchRequired)
	assert.False(t, reconcileSkipped)
	assert.Same(t, newProviderSpec, gotProviderSpec)
	assert.Empty(t, rhcosVersion)
}
