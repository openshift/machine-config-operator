package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	expectedImagePullspecWithTag string = "registry.hostname.com/org/repo:latest"
)

// This test ensures that cleanups for one build do not interfere with the
// objects for another build.
func TestPreparer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	lobj1 := fixtures.NewLayeredObjectsForTest("worker")
	lobj2 := fixtures.NewLayeredObjectsForTest("second-worker")

	kubeclient, mcfgclient, _ := fixtures.GetClientsForTestWithAdditionalObjects([]runtime.Object{}, lobj2.ToRuntimeObjects())

	kubeassert := framework.Assert(t, time.Millisecond, kubeclient, mcfgclient)

	// Create two preparers assigned to their own MachineOSBuild though sharing
	// the same kubeclient and mcfgclient objects.
	p1 := NewPreparer(kubeclient, mcfgclient, lobj1.MachineOSBuild, lobj1.MachineOSConfig)
	p2 := NewPreparer(kubeclient, mcfgclient, lobj2.MachineOSBuild, lobj2.MachineOSConfig)

	// Create two cleaners assigned to their own MachineOSBuilds though
	// sharing the same kubeclient and mcfgclient objects.
	//
	// Note: c2 purposely passes nil in to test the path where objects should
	// still be removed even if only the MachineOSBuild is available.
	c1 := NewCleaner(kubeclient, mcfgclient, lobj1.MachineOSBuild, lobj1.MachineOSConfig)
	c2 := NewCleaner(kubeclient, mcfgclient, lobj2.MachineOSBuild, nil)

	expectedConfigMaps := []string{
		"containerfile-worker-afc35db0f874c9bfdc586e6ba39f1504",
		"mc-worker-afc35db0f874c9bfdc586e6ba39f1504",
		"containerfile-second-worker-afc35db0f874c9bfdc586e6ba39f1504",
		"mc-second-worker-afc35db0f874c9bfdc586e6ba39f1504",
	}

	expectedSecrets := []string{
		"base-worker-afc35db0f874c9bfdc586e6ba39f1504",
		"final-worker-afc35db0f874c9bfdc586e6ba39f1504",
		"base-second-worker-afc35db0f874c9bfdc586e6ba39f1504",
		"final-second-worker-afc35db0f874c9bfdc586e6ba39f1504",
	}

	t.Run("Preparers", func(t *testing.T) {
		// Run both preparers.
		_, err := p1.Prepare(ctx)
		assert.NoError(t, err)

		_, err = p2.Prepare(ctx)
		assert.NoError(t, err)

		// After preparing for both, ensure that the expected configmaps and secrets
		// are present for both MachineOSBuilds.
		for _, expectedConfigMap := range expectedConfigMaps {
			kubeassert.ConfigMapIsCreated(ctx, expectedConfigMap)
		}

		for _, expectedSecret := range expectedSecrets {
			kubeassert.SecretIsCreated(ctx, expectedSecret)
		}
	})

	t.Run("Cleaners", func(t *testing.T) {
		// Cleanup the ephemeral objects from the first MachineOSBuild.
		assert.NoError(t, c1.Clean(ctx))

		// Ensure that only the objects from the first MachineOSBuild are gone and
		// that the other objects remain.
		for _, expectedConfigMap := range expectedConfigMaps[0:1] {
			kubeassert.ConfigMapIsDeleted(ctx, expectedConfigMap)
		}

		for _, expectedConfigMap := range expectedConfigMaps[2:] {
			kubeassert.ConfigMapIsCreated(ctx, expectedConfigMap)
		}

		for _, expectedSecret := range expectedSecrets[0:1] {
			kubeassert.SecretIsDeleted(ctx, expectedSecret)
		}

		for _, expectedSecret := range expectedSecrets[2:] {
			kubeassert.SecretIsCreated(ctx, expectedSecret)
		}

		// Next, clean up the ephemeral objects from the second MachineOSBuild.
		assert.NoError(t, c2.Clean(ctx))

		// This time, ensure that *all* ephemeral objects are gone.
		for _, expectedConfigMap := range expectedConfigMaps {
			kubeassert.ConfigMapIsDeleted(ctx, expectedConfigMap)
		}

		for _, expectedSecret := range expectedSecrets {
			kubeassert.SecretIsDeleted(ctx, expectedSecret)
		}
	})
}
