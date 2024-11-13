package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestPodImageBuilder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	podPhases := []corev1.PodPhase{
		corev1.PodPending,
		corev1.PodRunning,
		corev1.PodSucceeded,
		corev1.PodFailed,
	}

	for _, deletion := range []bool{true, false} {
		kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
		kubeassert = kubeassert.WithContext(ctx)

		pim := NewPodImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

		assert.NoError(t, pim.Start(ctx))

		buildPodName := utils.GetBuildPodName(lobj.MachineOSBuild)

		kubeassert.Now().PodExists(buildPodName)
		assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, pim.(*podImageBuilder).buildrequest)

		if deletion {
			now := metav1.Now()
			fixtures.SetPodDeletionTimestamp(ctx, t, kubeclient, lobj.MachineOSBuild, &now)
		} else {
			fixtures.SetPodDeletionTimestamp(ctx, t, kubeclient, lobj.MachineOSBuild, nil)
		}

		for _, podPhase := range podPhases {
			fixtures.SetPodPhase(ctx, t, kubeclient, lobj.MachineOSBuild, podPhase)
			assertObserverCanGetPodStatus(ctx, t, pim, podPhase, deletion)

			obs := NewPodImageBuildObserver(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)
			assertObserverCanGetPodStatus(ctx, t, obs, podPhase, deletion)

			pod, err := kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, buildPodName, metav1.GetOptions{})
			require.NoError(t, err)

			builder, err := buildrequest.NewBuilder(pod)
			require.NoError(t, err)

			obsbuilder := NewPodImageBuildObserverFromBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig, builder)
			assertObserverCanGetPodStatus(ctx, t, obsbuilder, podPhase, deletion)
		}

		assert.NoError(t, pim.Clean(ctx))

		kubeassert.Now().PodDoesNotExist(buildPodName)

		assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, pim.(*podImageBuilder).buildrequest)

		assert.NoError(t, pim.Stop(ctx))
	}
}

// Ensures that the build states are appropriately mapped within the common
// helper library for interrogating MachineOSBuild state.
func assertMachineOSBuildStateMapsToCommonState(ctx context.Context, t *testing.T, obs ImageBuildObserver) {
	t.Helper()

	mosbStatus, err := obs.MachineOSBuildStatus(ctx)
	assert.NoError(t, err)

	buildprogress, err := obs.Status(ctx)
	assert.NoError(t, err)

	mosbState := ctrlcommon.NewMachineOSBuildStateFromStatus(mosbStatus)

	// These are states where the MachineOSBuild may transition from either these
	// states or to a terminal state.
	transientBuildStates := map[mcfgv1alpha1.BuildProgress]struct{}{
		mcfgv1alpha1.MachineOSBuildPrepared: {},
		mcfgv1alpha1.MachineOSBuilding:      {},
	}

	// A terminal state is one where the MachineOSBuild cannot transition to any
	// other state. It is considered the "final" state.
	terminalBuildStates := map[mcfgv1alpha1.BuildProgress]struct{}{
		mcfgv1alpha1.MachineOSBuildFailed:      {},
		mcfgv1alpha1.MachineOSBuildSucceeded:   {},
		mcfgv1alpha1.MachineOSBuildInterrupted: {},
	}

	// Map of the build state to each function that should return true when the
	// MachineOSBuild is in that particular state.
	mosbStateFuncs := map[mcfgv1alpha1.BuildProgress]func() bool{
		mcfgv1alpha1.MachineOSBuildPrepared:    mosbState.IsBuildPrepared,
		mcfgv1alpha1.MachineOSBuilding:         mosbState.IsBuilding,
		mcfgv1alpha1.MachineOSBuildFailed:      mosbState.IsBuildFailure,
		mcfgv1alpha1.MachineOSBuildSucceeded:   mosbState.IsBuildSuccess,
		mcfgv1alpha1.MachineOSBuildInterrupted: mosbState.IsBuildInterrupted,
	}

	// Iterate through all of the known states and call the function from the helper library.
	for state, mosbStateFunc := range mosbStateFuncs {
		if state == buildprogress {
			assert.True(t, mosbStateFunc())
		} else {
			assert.False(t, mosbStateFunc())
		}

		if _, ok := transientBuildStates[buildprogress]; ok {
			assert.True(t, mosbState.IsInTransientState())
			assert.False(t, mosbState.IsInInitialState())
			assert.False(t, mosbState.IsInTerminalState())
		}

		if _, ok := terminalBuildStates[buildprogress]; ok {
			assert.False(t, mosbState.IsInTransientState())
			assert.False(t, mosbState.IsInInitialState())
			assert.True(t, mosbState.IsInTerminalState())
		}
	}
}

func assertObserverCanGetPodStatus(ctx context.Context, t *testing.T, obs ImageBuildObserver, podPhase corev1.PodPhase, deletion bool) {
	t.Helper()

	mosbStatus, err := obs.MachineOSBuildStatus(ctx)
	assert.NoError(t, err)

	buildprogress, err := obs.Status(ctx)
	assert.NoError(t, err)

	buildprogressToPodPhases := map[mcfgv1alpha1.BuildProgress]corev1.PodPhase{
		mcfgv1alpha1.MachineOSBuildPrepared:  corev1.PodPending,
		mcfgv1alpha1.MachineOSBuilding:       corev1.PodRunning,
		mcfgv1alpha1.MachineOSBuildFailed:    corev1.PodFailed,
		mcfgv1alpha1.MachineOSBuildSucceeded: corev1.PodSucceeded,
	}

	if deletion && (podPhase == corev1.PodPending || podPhase == corev1.PodRunning) {
		// If the pod is being deleted and it is in either the pending or running
		// phase, it should map to MachineOSBuildInterrupted.
		assert.Equal(t, mcfgv1alpha1.MachineOSBuildInterrupted, buildprogress)
	} else {
		// Otherwise, it should map to the above state.
		assert.Equal(t, buildprogressToPodPhases[buildprogress], podPhase)
	}

	assert.True(t, apihelpers.IsMachineOSBuildConditionTrue(mosbStatus.Conditions, buildprogress))

	assert.NotNil(t, mosbStatus.BuilderReference)

	assert.NotNil(t, mosbStatus.BuildStart)

	if podPhase == corev1.PodSucceeded {
		assert.Equal(t, "registry.hostname.com/org/repo@sha256:e1992921cba73d9e74e46142eca5946df8a895bfd4419fc8b5c6422d5e7192e6", mosbStatus.FinalImagePushspec)
		assert.NotNil(t, mosbStatus.BuildEnd)
	}

	assertMachineOSBuildStateMapsToCommonState(ctx, t, obs)
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
