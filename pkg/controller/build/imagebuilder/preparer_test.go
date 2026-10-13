package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

// This test ensures that cleanups for one build do not interfere with the
// objects for another build.
func TestPreparer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	obj1 := fixtures.NewObjectsForTest("worker")
	obj2 := fixtures.NewObjectsForTest("second-worker")
	obj3 := fixtures.NewObjectsForTest("third-worker")

	addlObjects := append(obj2.ToRuntimeObjects(), obj3.ToRuntimeObjects()...)
	kubeclient, mcfgclient, _, _, _, kubeassert := fixtures.GetClientsForTestWithAdditionalObjects(t, []runtime.Object{}, addlObjects)
	kubeassert = kubeassert.WithContext(ctx).Now()

	// Create three preparers assigned to their own MachineOSBuild though sharing
	// the same kubeclient and mcfgclient objects.
	p1 := NewPreparer(kubeclient, mcfgclient, obj1.MachineOSBuild, obj1.MachineOSConfig)
	p2 := NewPreparer(kubeclient, mcfgclient, obj2.MachineOSBuild, obj2.MachineOSConfig)
	p3 := NewPreparer(kubeclient, mcfgclient, obj3.MachineOSBuild, obj3.MachineOSConfig)

	// Run all of the preparers and ensure that all of the build objects have been created.
	br1, err := p1.Prepare(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, br1)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br1)
	err = setOwnerForObjects(ctx, kubeclient, br1)
	assert.NoError(t, err)

	br2, err := p2.Prepare(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, br2)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br2)
	err = setOwnerForObjects(ctx, kubeclient, br2)
	assert.NoError(t, err)

	br3, err := p3.Prepare(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, br3)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br3)
	err = setOwnerForObjects(ctx, kubeclient, br3)
	assert.NoError(t, err)

	// Create three cleaners assigned to their own MachineOSBuilds though
	// sharing the same kubeclient and mcfgclient objects.
	c1 := newCleaner(kubeclient, mcfgclient, obj1.MachineOSBuild, obj1.MachineOSConfig)
	// c2 purposely passes nil in to test the path where ephemeral build objects
	// should still be removed even if only the MachineOSBuild is available.
	c2 := newCleaner(kubeclient, mcfgclient, obj2.MachineOSBuild, nil)
	// c3 uses the Builder object from the BuildRequest instead so that we can
	// ensure that ephemeral build objects will be removed even if only the Builder object
	// is available.
	builder3 := br3.Builder()
	// Set the UID for the builder so that we can ensure that ephemeral build objects
	// will be removed even if only the Builder object is available.
	builder3.SetUID(types.UID(fixtures.JobUID))
	c3 := newCleanerFromBuilder(kubeclient, mcfgclient, builder3)

	// Cleanup the ephemeral objects from the first MachineOSBuild.
	assert.NoError(t, c1.Clean(ctx))

	// Ensure that only the objects from the first MachineOSBuild are gone and
	// that the other objects remain.
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, br1)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br2)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br3)

	// Next, clean up the ephemeral objects from the second MachineOSBuild.
	assert.NoError(t, c2.Clean(ctx))

	// Ensure that only the objects from the second MachineOSBuild are gone and
	// that the ones from the third MachineOSBuild remain.
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, br2)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br3)

	// Next, clean up the ephemeral objects from the third MachineOSBuild.
	assert.NoError(t, c3.Clean(ctx))

	// Ensure that the objects from the third MachineOSBuild are gone.
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, br3)

	// Finally, ensure that all objects have been removed.
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, br1)
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, br2)
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, br3)
}

func assertObjectsAreCreatedByPreparer(ctx context.Context, t *testing.T, kubeassert *testhelpers.Assertions, br buildrequest.BuildRequest) {
	configmaps, err := br.ConfigMaps()
	require.NoError(t, err)

	secrets, err := br.Secrets()
	require.NoError(t, err)

	for _, expectedConfigMap := range configmaps {
		kubeassert.WithContext(ctx).Now().ConfigMapExists(expectedConfigMap.Name)
	}

	for _, expectedSecret := range secrets {
		kubeassert.WithContext(ctx).Now().SecretExists(expectedSecret.Name)
	}
}

func assertObjectsAreRemovedByCleaner(ctx context.Context, t *testing.T, kubeassert *testhelpers.Assertions, br buildrequest.BuildRequest) {
	configmaps, err := br.ConfigMaps()
	require.NoError(t, err)

	secrets, err := br.Secrets()
	require.NoError(t, err)

	for _, expectedConfigMap := range configmaps {
		kubeassert.WithContext(ctx).Now().ConfigMapDoesNotExist(expectedConfigMap.Name)
	}

	for _, expectedSecret := range secrets {
		kubeassert.WithContext(ctx).Now().SecretDoesNotExist(expectedSecret.Name)
	}
}

func setOwnerForObjects(ctx context.Context, kubeclient *fakecorev1client.Clientset, br buildrequest.BuildRequest) error {
	// Add a dummy job as the owner for testing purposes
	jobOwnerRef := metav1.OwnerReference{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       "test-job",
		UID:        types.UID(fixtures.JobUID),
	}

	configmaps, err := br.ConfigMaps()
	if err != nil {
		return err
	}
	for _, configmap := range configmaps {
		cm, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configmap.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		cm.SetOwnerReferences([]metav1.OwnerReference{jobOwnerRef})
		_, err = kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	secrets, err := br.Secrets()
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		secret, err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, secret.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		secret.SetOwnerReferences([]metav1.OwnerReference{jobOwnerRef})
		_, err = kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(ctx, secret, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}
