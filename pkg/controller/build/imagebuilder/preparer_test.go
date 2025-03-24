package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
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
	kubeclient, mcfgclient, _, kubeassert := fixtures.GetClientsForTestWithAdditionalObjects(t, []runtime.Object{}, addlObjects)
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

	br2, err := p2.Prepare(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, br2)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br2)

	br3, err := p3.Prepare(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, br3)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, br3)

	// Create three cleaners assigned to their own MachineOSBuilds though
	// sharing the same kubeclient and mcfgclient objects.
	c1 := newCleaner(kubeclient, mcfgclient, obj1.MachineOSBuild, obj1.MachineOSConfig)
	// c2 purposely passes nil in to test the path where ephemeral build objects
	// should still be removed even if only the MachineOSBuild is available.
	c2 := newCleaner(kubeclient, mcfgclient, obj2.MachineOSBuild, nil)
	// c3 uses the Builder object from the BuildRequest instead so that we can
	// ensure that ephemeral build objects will be removed even if only the Builder object
	// is available.

	build, err := br3.Builder()
	require.NoError(t, err)
	c3 := newCleanerFromBuilder(kubeclient, mcfgclient, build)

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
