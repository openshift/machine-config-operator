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
	"github.com/openshift/machine-config-operator/test/framework"
)

func TestImageBuilder(t *testing.T) {
	t.Parallel()

	kubeclient, mcfgclient, lobj := fixtures.GetClientsForTest()
	kubeassert := framework.Assert(t, time.Millisecond, kubeclient, mcfgclient)

	pim := NewPodImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	assert.NoError(t, pim.Start(ctx))

	buildPodName := utils.GetBuildPodName(lobj.MachineOSBuild)

	kubeassert.BuildPodIsCreated(ctx, buildPodName)

	podPhases := []corev1.PodPhase{
		corev1.PodPending,
		corev1.PodRunning,
		corev1.PodSucceeded,
		corev1.PodFailed,
	}

	buildprogressToPodPhases := map[mcfgv1alpha1.BuildProgress]corev1.PodPhase{
		mcfgv1alpha1.MachineOSBuildPrepared:  corev1.PodPending,
		mcfgv1alpha1.MachineOSBuilding:       corev1.PodRunning,
		mcfgv1alpha1.MachineOSBuildFailed:    corev1.PodFailed,
		mcfgv1alpha1.MachineOSBuildSucceeded: corev1.PodSucceeded,
	}

	for _, podPhase := range podPhases {
		fixtures.SetPodPhase(ctx, t, kubeclient, lobj.MachineOSBuild, podPhase)

		buildprogress, err := pim.Status(ctx)
		require.NoError(t, err)

		assert.Equal(t, buildprogressToPodPhases[buildprogress], podPhase)

		mosbStatus, err := pim.MachineOSBuildStatus(ctx)
		require.NoError(t, err)

		assert.True(t, apihelpers.IsMachineOSBuildConditionTrue(mosbStatus.Conditions, buildprogress))

		assert.NotNil(t, mosbStatus.BuilderReference)

		if podPhase == corev1.PodSucceeded {
			assert.NotNil(t, mosbStatus.BuildEnd)
			assert.Equal(t, "registry.hostname.com/org/repo@sha256:628e4e8f0a78d91015c6cebeee95931ae2e8defe5dfb4ced4a82830e08937573", mosbStatus.FinalImagePushspec)
		}
	}

	require.NoError(t, pim.Clean(ctx))

	kubeassert.BuildPodIsDeleted(ctx, buildPodName)

	require.NoError(t, pim.Stop(ctx))
}

func TestImageBuilderCanCleanWithOnlyMachineOSBuild(t *testing.T) {
	t.Parallel()

	kubeclient, mcfgclient, lobj := fixtures.GetClientsForTest()

	pim := NewPodImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	assert.NoError(t, pim.Start(ctx))

	buildPodName := utils.GetBuildPodName(lobj.MachineOSBuild)

	kubeassert := framework.Assert(t, time.Millisecond, kubeclient, mcfgclient)

	kubeassert.BuildPodIsCreated(ctx, buildPodName)

	cleaner := NewPodImageBuildCleaner(kubeclient, mcfgclient, lobj.MachineOSBuild)
	assert.NoError(t, cleaner.Clean(ctx))

	kubeassert.BuildPodIsDeleted(ctx, buildPodName)
}
