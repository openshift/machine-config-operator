package build

import (
	"context"
	"fmt"
	"io/ioutil"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Creates a simple MachineConfigPool object for testing.
func newMachineConfigPool(name string, params ...string) *mcfgv1.MachineConfigPool {
	renderedConfigName := ""
	if len(params) >= 1 {
		renderedConfigName = params[0]
	} else {
		renderedConfigName = fmt.Sprintf("rendered-%s-1", name)
	}

	cfg := mcfgv1.MachineConfigPoolStatusConfiguration{
		ObjectReference: corev1.ObjectReference{
			Name: renderedConfigName,
		},
		Source: []corev1.ObjectReference{
			{
				Name: name + "-config-1",
				Kind: "MachineConfig",
			},
			{
				Name: name + "-config-2",
				Kind: "MachineConfig",
			},
			{
				Name: name + "-config-3",
				Kind: "MachineConfig",
			},
		},
	}

	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{},
			Labels:      map[string]string{},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: cfg,
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: cfg,
			Conditions:    []mcfgv1.MachineConfigPoolCondition{},
		},
	}
}

// Opts a MachineConfigPool into layering.
func optInMCP(ctx context.Context, t *testing.T, bcc *buildControllerClientset, poolName string) *mcfgv1.MachineConfigPool {
	t.Helper()

	mcp, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mcp.Labels = map[string]string{
		ctrlcommon.LayeringEnabledPoolLabel: "",
	}

	mcp, err = bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	return mcp
}

// Opts a MachineConfigPool out of layering.
func optOutMCP(ctx context.Context, t *testing.T, bcc *buildControllerClientset, poolName string) {
	t.Helper()

	mcp, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	delete(mcp.Labels, ctrlcommon.LayeringEnabledPoolLabel)

	_, err = bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// Polls until a MachineConfigPool reaches a desired state.
func assertMachineConfigPoolReachesState(ctx context.Context, t *testing.T, bcc *buildControllerClientset, poolName string, checkFunc func(*mcfgv1.MachineConfigPool) bool) bool {
	t.Helper()

	pollCtx, cancel := context.WithTimeout(ctx, time.Second*10)
	t.Cleanup(cancel)

	err := wait.PollImmediateUntilWithContext(pollCtx, time.Millisecond, func(c context.Context) (bool, error) {
		mcp, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Get(c, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return checkFunc(mcp), nil
	})

	return assert.NoError(t, err, "MachineConfigPool %s never reached desired state", poolName)
}

// Asserts that there are no build pods.
func assertNoBuildPods(ctx context.Context, t *testing.T, bcc *buildControllerClientset) bool {
	t.Helper()

	foundBuildPods := false

	buildPodNames := []string{}

	podList, err := bcc.KubeClient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, pod := range podList.Items {
		pod := pod
		if isBuildPod(&pod) {
			foundBuildPods = true
			buildPodNames = append(buildPodNames, pod.Name)
		}
	}

	return assert.False(t, foundBuildPods, "expected not to find build pods, found: %v", buildPodNames)
}

// Polls until a build pod is created.
func assertBuildPodIsCreated(ctx context.Context, t *testing.T, bcc *buildControllerClientset, buildPodName string) bool {
	t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		podList, err := bcc.KubeClient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, pod := range podList.Items {
			if pod.Name == buildPodName {
				return true, nil
			}
		}

		return false, nil
	})

	return assert.NoError(t, err, "build pod %s was not created", buildPodName)
}

// Simulates a pod being scheduled and reaching various states. Verifies that
// the target MachineConfigPool reaches the expected states as it goes.
func assertMCPFollowsBuildPodStatus(ctx context.Context, t *testing.T, bcc *buildControllerClientset, mcp *mcfgv1.MachineConfigPool, endingPhase corev1.PodPhase) (outcome bool) {
	t.Helper()

	defer func() {
		assert.True(t, outcome)
	}()

	// Each of the various pod phases we're interested in.
	podPhases := []corev1.PodPhase{
		corev1.PodPending,
		corev1.PodRunning,
		endingPhase,
	}

	// Each pod phase is correllated to a MachineConfigPoolConditionType.
	podPhaseToMCPCondition := map[corev1.PodPhase]mcfgv1.MachineConfigPoolConditionType{
		corev1.PodPending:   mcfgv1.MachineConfigPoolBuildPending,
		corev1.PodRunning:   mcfgv1.MachineConfigPoolBuilding,
		corev1.PodFailed:    mcfgv1.MachineConfigPoolBuildFailed,
		corev1.PodSucceeded: mcfgv1.MachineConfigPoolBuildSuccess,
	}

	// Determine if the MachineConfigPool should have a reference to the build pod.
	shouldHaveBuildPodRef := map[corev1.PodPhase]bool{
		corev1.PodPending:   true,
		corev1.PodRunning:   true,
		corev1.PodFailed:    true,
		corev1.PodSucceeded: false,
	}

	buildPodName := getBuildPodName(mcp)

	// Wait for the build pod to be created.
	outcome = assertBuildPodIsCreated(ctx, t, bcc, buildPodName)
	if !outcome {
		return
	}

	// Cycle through each of the build pod phases.
	for _, phase := range podPhases {
		// Get the build pod by name.
		buildPod, err := bcc.KubeClient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, buildPodName, metav1.GetOptions{})
		require.NoError(t, err)

		// Set the pod phase and update it.
		buildPod.Status.Phase = phase
		_, err = bcc.KubeClient.CoreV1().Pods(ctrlcommon.MCONamespace).Update(ctx, buildPod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Look up the expected MCP condition for our current pod phase.
		expectedMCPCondition := podPhaseToMCPCondition[phase]

		// Look up the expected build pod condition for our current pod phase.
		expectedBuildPodRefPresence := shouldHaveBuildPodRef[phase]

		// Wait for the MCP condition to reach the expected state.
		outcome = assertMachineConfigPoolReachesState(ctx, t, bcc, mcp.Name, func(mcp *mcfgv1.MachineConfigPool) bool {
			return mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, expectedMCPCondition) &&
				expectedBuildPodRefPresence == machineConfigPoolHasBuildPodRef(mcp)
		})

		if !outcome {
			return false
		}
	}

	// Find out what happened to the build pod.
	_, err := bcc.KubeClient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, buildPodName, metav1.GetOptions{})
	switch endingPhase {
	case corev1.PodSucceeded:
		// If the build pod was successful, looking it up should fail because it should have been deleted.
		outcome = assert.Error(t, err)
	case corev1.PodFailed:
		// If the build pod failed, looking it up should succeed since we leave it around for debugging.
		outcome = assert.NoError(t, err)
	}

	return
}

// Dumps all the objects within each of the fake clients to a YAML file for easy debugging.
func dumpObjects(ctx context.Context, t *testing.T, bcc *buildControllerClientset, filenamePrefix string) {
	if bcc.ImageClient != nil {
		images, err := bcc.ImageClient.ImageV1().Images().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, images, filenamePrefix+"-images.yaml")

		imagestreams, err := bcc.ImageClient.ImageV1().ImageStreams(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, imagestreams, filenamePrefix+"-imagestreams.yaml")

		imagestreamtags, err := bcc.ImageClient.ImageV1().ImageStreamTags(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, imagestreamtags, filenamePrefix+"-imagestreamtags.yaml")
	}

	if bcc.McfgClient != nil {
		mcp, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, mcp, filenamePrefix+"-machineconfigpools.yaml")

		machineconfigs, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigs().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, machineconfigs, filenamePrefix+"-machineconfigs.yaml")
	}

	if bcc.KubeClient != nil {
		pods, err := bcc.KubeClient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, pods, filenamePrefix+"-pods.yaml")
	}

	if bcc.BuildClient != nil {
		buildconfigs, err := bcc.BuildClient.BuildV1().BuildConfigs(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, buildconfigs, filenamePrefix+"-buildconfigs.yaml")

		builds, err := bcc.BuildClient.BuildV1().Builds(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		dumpToYAMLFile(t, builds, filenamePrefix+"-builds.yaml")
	}
}

func dumpToYAMLFile(t *testing.T, obj interface{}, filename string) {
	out, err := yaml.Marshal(obj)
	require.NoError(t, err)

	require.NoError(t, ioutil.WriteFile(filename, out, 0755))
}
