package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// These consts are just for testing purposed as Jobs don't have a phase field similar to pods
const (
	jobPending   = "pending"
	jobRunning   = "running"
	jobSucceeded = "succeeded"
	jobFailed    = "failed"
)

func TestJobImageBuilder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
	kubeassert = kubeassert.WithContext(ctx)

	jim := NewJobImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	assert.NoError(t, jim.Start(ctx))

	buildJobName := utils.GetBuildJobName(lobj.MachineOSBuild)

	kubeassert.Now().JobExists(buildJobName)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, jim.(*jobImageBuilder).buildrequest)

	jobStatuses := []struct {
		testName string
		jobPhase string
		js       fixtures.JobStatus
	}{
		{
			testName: "Build in prepared state & Job pending",
			jobPhase: jobPending,
			js: fixtures.JobStatus{
				Active:    0,
				Succeeded: 0,
				Failed:    0,
			},
		},
		{
			testName: "Build is in progress & Job is running",
			jobPhase: jobRunning,
			js: fixtures.JobStatus{
				Active:    1,
				Succeeded: 0,
				Failed:    0,
			},
		},
		{
			testName: "Build in progress after first failure that has not been counted yet",
			jobPhase: jobRunning,
			js: fixtures.JobStatus{
				Active:                        0,
				Succeeded:                     0,
				Failed:                        0,
				UncountedTerminatedPodsFailed: "testuid-1234",
			},
		},
		{
			testName: "Build in progress after 2 failures",
			jobPhase: jobRunning,
			js: fixtures.JobStatus{
				Active:    0,
				Succeeded: 0,
				Failed:    2,
			},
		},
		{
			testName: "Build has succeed & Job is complete",
			jobPhase: jobSucceeded,
			js: fixtures.JobStatus{
				Active:    0,
				Succeeded: 1,
				Failed:    0,
			},
		},
		{
			testName: "Build has failed & Job is in failed state",
			jobPhase: jobFailed,
			js: fixtures.JobStatus{
				Active:    0,
				Succeeded: 0,
				Failed:    4,
			},
		},
	}

	for _, jobStatus := range jobStatuses {
		fixtures.SetJobStatus(ctx, t, kubeclient, lobj.MachineOSBuild, jobStatus.js)
		assertObserverCanGetJobStatus(ctx, t, jim, jobStatus.jobPhase)

		obs := NewJobImageBuildObserver(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)
		assertObserverCanGetJobStatus(ctx, t, obs, jobStatus.jobPhase)

		job, err := kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Get(ctx, buildJobName, metav1.GetOptions{})
		require.NoError(t, err)

		builder, err := buildrequest.NewBuilder(job)
		require.NoError(t, err)

		obsbuilder := NewJobImageBuildObserverFromBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig, builder)
		assertObserverCanGetJobStatus(ctx, t, obsbuilder, jobStatus.jobPhase)
	}

	require.NoError(t, jim.Clean(ctx))

	kubeassert.Now().JobDoesNotExist(buildJobName)

	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, jim.(*jobImageBuilder).buildrequest)

	require.NoError(t, jim.Stop(ctx))
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
	transientBuildStates := map[mcfgv1.BuildProgress]struct{}{
		mcfgv1.MachineOSBuildPrepared: {},
		mcfgv1.MachineOSBuilding:      {},
	}

	// A terminal state is one where the MachineOSBuild cannot transition to any
	// other state. It is considered the "final" state.
	terminalBuildStates := map[mcfgv1.BuildProgress]struct{}{
		mcfgv1.MachineOSBuildFailed:      {},
		mcfgv1.MachineOSBuildSucceeded:   {},
		mcfgv1.MachineOSBuildInterrupted: {},
	}

	// Map of the build state to each function that should return true when the
	// MachineOSBuild is in that particular state.
	mosbStateFuncs := map[mcfgv1.BuildProgress]func() bool{
		mcfgv1.MachineOSBuildPrepared:    mosbState.IsBuildPrepared,
		mcfgv1.MachineOSBuilding:         mosbState.IsBuilding,
		mcfgv1.MachineOSBuildFailed:      mosbState.IsBuildFailure,
		mcfgv1.MachineOSBuildSucceeded:   mosbState.IsBuildSuccess,
		mcfgv1.MachineOSBuildInterrupted: mosbState.IsBuildInterrupted,
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

func assertObserverCanGetJobStatus(ctx context.Context, t *testing.T, obs ImageBuildObserver, jobPhase string) {
	buildprogressToJobPhases := map[mcfgv1.BuildProgress]string{
		mcfgv1.MachineOSBuildPrepared:  jobPending,
		mcfgv1.MachineOSBuilding:       jobRunning,
		mcfgv1.MachineOSBuildFailed:    jobFailed,
		mcfgv1.MachineOSBuildSucceeded: jobSucceeded,
	}

	buildprogress, err := obs.Status(ctx)
	require.NoError(t, err)

	assert.Equal(t, buildprogressToJobPhases[buildprogress], jobPhase)

	mosbStatus, err := obs.MachineOSBuildStatus(ctx)
	require.NoError(t, err)

	assert.True(t, apihelpers.IsMachineOSBuildConditionTrue(mosbStatus.Conditions, buildprogress))

	assert.NotNil(t, mosbStatus.Builder)

	assert.NotNil(t, mosbStatus.BuildStart)

	if jobPhase == jobSucceeded {
		assert.NotNil(t, mosbStatus.BuildEnd)
		assert.Equal(t, "registry.hostname.com/org/repo@sha256:e1992921cba73d9e74e46142eca5946df8a895bfd4419fc8b5c6422d5e7192e6", string(mosbStatus.DigestedImagePushSpec))
	}

	assertMachineOSBuildStateMapsToCommonState(ctx, t, obs)
}

func TestJobImageBuilderCanCleanWithOnlyMachineOSBuild(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
	kubeassert = kubeassert.WithContext(ctx)

	jim := NewJobImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	assert.NoError(t, jim.Start(ctx))

	buildJobName := utils.GetBuildJobName(lobj.MachineOSBuild)

	kubeassert.JobExists(buildJobName)
	assertObjectsAreCreatedByPreparer(ctx, t, kubeassert, jim.(*jobImageBuilder).buildrequest)

	cleaner := NewJobImageBuildCleaner(kubeclient, mcfgclient, lobj.MachineOSBuild)
	assert.NoError(t, cleaner.Clean(ctx))

	kubeassert.JobDoesNotExist(buildJobName)
	assertObjectsAreRemovedByCleaner(ctx, t, kubeassert, jim.(*jobImageBuilder).buildrequest)
}

func TestJobImageBuilderSetsBuildStartAndEndTimestamp(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
	kubeassert = kubeassert.WithContext(ctx)

	jim := NewJobImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig)

	assert.NoError(t, jim.Start(ctx))

	job, err := kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildJobName(lobj.MachineOSBuild), metav1.GetOptions{})
	require.NoError(t, err)

	// Set -60 seconds so that we can get a time that is one minute before the current time.
	jobStartTime := time.Now().Add(time.Second * -60)
	job.SetCreationTimestamp(metav1.NewTime(jobStartTime))

	_, err = kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Update(ctx, job, metav1.UpdateOptions{})
	require.NoError(t, err)

	fixtures.SetJobStatus(ctx, t, kubeclient, lobj.MachineOSBuild, fixtures.JobStatus{Succeeded: 1})

	status, err := jim.MachineOSBuildStatus(ctx)
	require.NoError(t, err)

	assert.NotNil(t, status.BuildStart)
	assert.NotNil(t, status.BuildEnd)

	assert.True(t, status.BuildStart.Before(status.BuildEnd))
	assert.GreaterOrEqual(t, status.BuildEnd.Time.Sub(status.BuildStart.Time), time.Second*60)
	assert.Equal(t, status.BuildStart.Time, jobStartTime)
}
