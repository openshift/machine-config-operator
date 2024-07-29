package buildrequest

import (
	"context"
	"testing"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	lobj1 := newLayeredObjectsForTest("worker")
	lobj2 := newLayeredObjectsForTest("second-worker")

	kubeclient, mcfgclient := getClientsForTest(addlObjects{
		mcfgObjects: lobj2.toRuntimeObjects(),
	})

	// Create two preparers assigned to their own MachineOSBuild though sharing
	// the same kubeclient and mcfgclient objects.
	p1 := NewPreparer(kubeclient, mcfgclient, lobj1.mosb, lobj1.mosc)
	p2 := NewPreparer(kubeclient, mcfgclient, lobj2.mosb, lobj2.mosc)

	// Run both preparers.
	_, err := p1.Prepare(ctx)
	assert.NoError(t, err)

	_, err = p2.Prepare(ctx)
	assert.NoError(t, err)

	expectedConfigMaps := []string{
		"containerfile-rendered-worker-1",
		"mc-rendered-worker-1",
		"containerfile-rendered-second-worker-1",
		"mc-rendered-second-worker-1",
	}

	expectedSecrets := []string{
		"base-rendered-worker-1",
		"final-rendered-worker-1",
		"base-rendered-second-worker-1",
		"final-rendered-second-worker-1",
	}

	// After preparing for both, ensure that the expected configmaps and secrets
	// are present for both MachineOSBuilds.
	for _, expectedConfigMap := range expectedConfigMaps {
		_, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, expectedConfigMap, metav1.GetOptions{})
		require.NoError(t, err)
	}

	for _, expectedSecret := range expectedSecrets {
		_, err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, expectedSecret, metav1.GetOptions{})
		require.NoError(t, err)
	}

	// Cleanup the ephemeral objects from the first MachineOSBuild.
	assert.NoError(t, p1.Clean(ctx))

	// Ensure that only the objects from the first MachineOSBuild are gone and
	// that the other objects remain.
	for _, expectedConfigMap := range expectedConfigMaps[0:1] {
		_, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, expectedConfigMap, metav1.GetOptions{})
		assert.NotNil(t, err)
		assert.True(t, k8serrors.IsNotFound(err))
	}

	for _, expectedConfigMap := range expectedConfigMaps[2:] {
		_, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, expectedConfigMap, metav1.GetOptions{})
		require.NoError(t, err)
	}

	for _, expectedSecret := range expectedSecrets[0:1] {
		_, err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, expectedSecret, metav1.GetOptions{})
		assert.NotNil(t, err)
		assert.True(t, k8serrors.IsNotFound(err))
	}

	for _, expectedSecret := range expectedSecrets[2:] {
		_, err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, expectedSecret, metav1.GetOptions{})
		require.NoError(t, err)
	}

	// Next, clean up the ephemeral objects from the second MachineOSBuild.
	assert.NoError(t, p2.Clean(ctx))

	// This time, ensure that *all* ephemeral objects are gone.
	for _, expectedConfigMap := range expectedConfigMaps {
		_, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, expectedConfigMap, metav1.GetOptions{})
		assert.NotNil(t, err)
		assert.True(t, k8serrors.IsNotFound(err))
	}

	for _, expectedSecret := range expectedSecrets {
		_, err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, expectedSecret, metav1.GetOptions{})
		assert.NotNil(t, err)
		assert.True(t, k8serrors.IsNotFound(err))
	}
}
