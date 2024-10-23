package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
)

func TestPodImageBuilder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
	kubeassert = kubeassert.WithContext(ctx)

	pim := NewPodImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	assert.NoError(t, pim.Start(ctx))

	buildPodName := utils.GetBuildPodName(lobj.MachineOSBuild)

	kubeassert.Now().PodExists(buildPodName)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, pim.(*podImageBuilder).buildrequest)

	podPhases := []corev1.PodPhase{
		corev1.PodPending,
		corev1.PodRunning,
		corev1.PodSucceeded,
		corev1.PodFailed,
	}

	for _, podPhase := range podPhases {
		fixtures.SetPodPhase(ctx, t, kubeclient, lobj.MachineOSBuild, podPhase)
		assertObserverCanGetPodStatus(ctx, t, pim, podPhase)

		obs := NewPodImageBuildObserver(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)
		assertObserverCanGetPodStatus(ctx, t, obs, podPhase)
	}

	require.NoError(t, pim.Clean(ctx))

	kubeassert.Now().PodDoesNotExist(buildPodName)

	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, pim.(*podImageBuilder).buildrequest)

	require.NoError(t, pim.Stop(ctx))
}

func assertObserverCanGetPodStatus(ctx context.Context, t *testing.T, obs ImageBuildObserver, podPhase corev1.PodPhase) {
	buildprogressToPodPhases := map[mcfgv1alpha1.BuildProgress]corev1.PodPhase{
		mcfgv1alpha1.MachineOSBuildPrepared:  corev1.PodPending,
		mcfgv1alpha1.MachineOSBuilding:       corev1.PodRunning,
		mcfgv1alpha1.MachineOSBuildFailed:    corev1.PodFailed,
		mcfgv1alpha1.MachineOSBuildSucceeded: corev1.PodSucceeded,
	}

	buildprogress, err := obs.Status(ctx)
	require.NoError(t, err)

	assert.Equal(t, buildprogressToPodPhases[buildprogress], podPhase)

	mosbStatus, err := obs.MachineOSBuildStatus(ctx)
	require.NoError(t, err)

	assert.True(t, apihelpers.IsMachineOSBuildConditionTrue(mosbStatus.Conditions, buildprogress))

	assert.NotNil(t, mosbStatus.BuilderReference)

	if podPhase == corev1.PodSucceeded {
		assert.NotNil(t, mosbStatus.BuildEnd)
		assert.Equal(t, "registry.hostname.com/org/repo@sha256:628e4e8f0a78d91015c6cebeee95931ae2e8defe5dfb4ced4a82830e08937573", mosbStatus.FinalImagePushspec)
	}
}

func TestPodImageBuilderCanCleanWithOnlyMachineOSBuild(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
	kubeassert = kubeassert.WithContext(ctx)

	pim := NewPodImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	assert.NoError(t, pim.Start(ctx))

	buildPodName := utils.GetBuildPodName(lobj.MachineOSBuild)

	kubeassert.PodExists(buildPodName)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, pim.(*podImageBuilder).buildrequest)

	cleaner := NewPodImageBuildCleaner(kubeclient, mcfgclient, lobj.MachineOSBuild)
	assert.NoError(t, cleaner.Clean(ctx))

	kubeassert.PodDoesNotExist(buildPodName)
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, pim.(*podImageBuilder).buildrequest)
}
