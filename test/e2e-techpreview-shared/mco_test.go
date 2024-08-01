package e2e_techpreview_shared

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This is a general smoke test of the MCO. This performs a number of
// concurrent subtests to ensure that the MCO is in a stable state. This is
// meant to be an example entrypoint into the NodeE2E-style test.
func TestMCOSmokeTest(t *testing.T) {
	cs := framework.NewClientSet("")

	t.Run("MachineConfigPools", func(t *testing.T) {
		t.Parallel()
		testMachineConfigPools(t, cs)
	})

	t.Run("Nodes", func(t *testing.T) {
		t.Parallel()

		testNodes(t, cs)
	})

	t.Run("Daemonsets", func(t *testing.T) {
		t.Parallel()
		testDaemonSets(t, cs)
	})

	t.Run("Deployments", func(t *testing.T) {
		t.Parallel()
		testDeployments(t, cs)
	})

	t.Run("Cluster Operators", func(t *testing.T) {
		t.Parallel()
		testClusterOperator(t, cs)
	})
}

// Tests that all expected daemonsets are running.
func testDaemonSets(t *testing.T, cs *framework.ClientSet) {
	names := []string{
		"machine-config-daemon",
		"machine-config-server",
	}

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertDaemonsetIsRunning(t, cs, name)
		})
	}
}

// Tests that all expected deployments are running.
func testDeployments(t *testing.T, cs *framework.ClientSet) {
	names := []string{
		"machine-config-operator",
		"machine-config-controller",
	}

	isOCBEnabled, err := isOnClusterBuildEnabled(cs)
	require.NoError(t, err)

	if isOCBEnabled {
		names = append(names, "machine-os-builder")
		t.Logf("On-cluster layering enabled, will also check for machine-os-builder")
	}

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertDeploymentIsRunning(t, cs, name)
		})
	}
}

// Tests that all nodes have the correct annotations and labels, that they have
// the correct OS, and provides ane entrypoint into running NodeE2E tests.
func testNodes(t *testing.T, cs *framework.ClientSet) {
	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	testBinaries, err := getPrebuiltTestBinaryOrBuildNow(t, cs)
	require.NoError(t, err)

	for _, node := range nodes.Items {
		node := node
		t.Run(node.Name, func(t *testing.T) {
			t.Parallel()
			t.Run("Annotations and Labels", func(t *testing.T) {
				t.Parallel()
				assertNodeHasExpectedLabelsAndAnnotations(t, &node)
			})
			t.Run("Node OS", func(t *testing.T) {
				t.Parallel()
				assertNodeIsInCorrectOS(t, cs, &node)
			})
			t.Run("Node Configs E2E", func(t *testing.T) {
				t.Parallel()

				outputDirname, err := helpers.ArtifactDirOrLocalDir(t)
				require.NoError(t, err)

				helpers.RunE2ETestsOnNode(t, cs, helpers.NodeE2ETestOpts{
					CollectOutputAsJunit: true,
					CollectOutput:        true,
					OutputDir:            outputDirname,
					Node:                 node,
					TestBinaries:         testBinaries,
				})
			})
		})
	}
}

func getPrebuiltTestBinaryOrBuildNow(t *testing.T, cs *framework.ClientSet) ([]*helpers.TestBinary, error) {
	testBinaryPath := filepath.Join(helpers.GetRepoRoot(), "test/e2e-node", "e2e-node-bin")
	testBinary, err := helpers.NewTestBinary("linux", "amd64", testBinaryPath)
	if err == nil {
		t.Logf("Using prebuilt test binary found under %s", testBinaryPath)
		return []*helpers.TestBinary{testBinary}, nil
	}

	return helpers.BuildE2EBinariesForNodes(t, cs)
}

// Tests that the MachineConfigPools are ready and that all referenced MachineConfigs exist.
func testMachineConfigPools(t *testing.T, cs *framework.ClientSet) {
	pools, err := cs.MachineconfigurationV1Interface.MachineConfigPools().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	for _, pool := range pools.Items {
		pool := pool
		t.Run(pool.Name, func(t *testing.T) {
			t.Parallel()
			t.Run("IsReady", func(t *testing.T) {
				assertMachineConfigPoolIsReady(t, &pool)
			})

			t.Run("MachineConfigsExist", func(t *testing.T) {
				assertMachineConfigsExist(t, cs, &pool)
			})
		})
	}
}

// Asserts that a given MachineConfigPool is ready.
func assertMachineConfigPoolIsReady(t *testing.T, mcp *mcfgv1.MachineConfigPool) {
	t.Helper()

	assert.NotEmpty(t, mcp.Spec.Configuration.Name, "expected rendered MachineConfig to be present on .spec.configuration")
	assert.NotEmpty(t, mcp.Status.Configuration.Name, "expected rendered MachineConfig to be present on .status.configuration")
	assert.Equal(t, mcp.Spec.Configuration, mcp.Status.Configuration)
	assert.Equal(t, mcp.Status.DegradedMachineCount, int32(0), "expected no degraded machines, got: %d", mcp.Status.DegradedMachineCount)
	assert.Equal(t, mcp.Status.MachineCount, mcp.Status.ReadyMachineCount,
		"all machines should be ready, machine count: %d, ready machine count: %d", mcp.Status.MachineCount, mcp.Status.ReadyMachineCount)

	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated), "expected %s to be true", mcfgv1.MachineConfigPoolUpdated)

	falseConditions := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolDegraded,
		mcfgv1.MachineConfigPoolRenderDegraded,
		mcfgv1.MachineConfigPoolUpdating,
		mcfgv1.MachineConfigPoolPinnedImageSetsDegraded,
	}

	for _, condition := range falseConditions {
		assert.False(t, apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condition), "expected %s to be false", condition)
	}
}

// Asserts that all MachineConfigs referenced by a MachineConfigPool exist.
func assertMachineConfigsExist(t *testing.T, cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) {
	t.Helper()

	machineConfigs := []string{
		mcp.Spec.Configuration.Name,
	}

	for _, src := range mcp.Spec.Configuration.Source {
		if src.Kind == "MachineConfig" {
			machineConfigs = append(machineConfigs, src.Name)
		}
	}

	for _, machineConfig := range machineConfigs {
		machineConfig := machineConfig
		t.Run(machineConfig, func(t *testing.T) {
			t.Parallel()
			assertMachineConfigExists(t, cs, machineConfig)
		})
	}
}

// Asserts that an individual MachineConfig exists.
func assertMachineConfigExists(t *testing.T, cs *framework.ClientSet, name string) {
	t.Helper()

	mc, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(context.TODO(), name, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, mc)
}

// Asserts that a given node is booted into the expected OS image.
func assertNodeIsInCorrectOS(t *testing.T, cs *framework.ClientSet, node *corev1.Node) {
	t.Helper()

	ne, err := helpers.NewNodeExecForNode(t, cs, *node)
	require.NoError(t, err)

	results, err := ne.ExecuteCommand(helpers.ExecOpts{
		Command: []string{"rpm-ostree", "status"},
	})

	currentImage, currentImageOK := node.Annotations[daemonconsts.CurrentImageAnnotationKey]
	if currentImageOK && currentImage != "" {
		assert.Contains(t, results.Combined.String(), currentImage)
		return
	}

	currentConfig, ok := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	if !ok {
		t.Fatalf("node %s does not have a current config", node.Name)
	}

	mc, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(context.TODO(), currentConfig, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Contains(t, results.Combined.String(), mc.Spec.OSImageURL)
}

// Asserts that a given node has the expected labels and annotations.
func assertNodeHasExpectedLabelsAndAnnotations(t *testing.T, node *corev1.Node) {
	t.Helper()

	expectedAnnotations := []string{
		daemonconsts.CurrentMachineConfigAnnotationKey,
		daemonconsts.DesiredMachineConfigAnnotationKey,
		daemonconsts.MachineConfigDaemonReasonAnnotationKey,
		daemonconsts.MachineConfigDaemonStateAnnotationKey,
	}

	for _, annoKey := range expectedAnnotations {
		_, ok := node.Annotations[annoKey]
		assert.True(t, ok, "expected node to have annotation %q", annoKey)
	}

	current := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	desired := node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
	assert.Equal(t, current, desired, "expected node machineconfigs to be equal")

	mcdState := node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey]
	assert.Equal(t, mcdState, daemonconsts.MachineConfigDaemonStateDone, "expected MachineConfigDaemon state to be %q, got: %q", daemonconsts.MachineConfigDaemonStateDone, mcdState)

	reason := node.Annotations[daemonconsts.MachineConfigDaemonReasonAnnotationKey]
	assert.Empty(t, reason, "expected %q to be empty, got: %q", daemonconsts.MachineConfigDaemonReasonAnnotationKey, reason)
}

// Asserts that a daemonset is running with the correct numbers.
func assertDaemonsetIsRunning(t *testing.T, cs *framework.ClientSet, name string) {
	t.Helper()

	ds, err := cs.AppsV1Interface.DaemonSets(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	require.NoError(t, err)

	number := ds.Status.DesiredNumberScheduled

	numbers := map[string]int32{
		"currentNumberScheduled": ds.Status.CurrentNumberScheduled,
		"desiredNumberScheduled": ds.Status.DesiredNumberScheduled,
		"numberAvailable":        ds.Status.NumberAvailable,
		"numberReady":            ds.Status.NumberReady,
	}

	for name, value := range numbers {
		assert.Equal(t, value, number, "expected %s to equal %d, got: %s", name, number, value)
	}

	pods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("k8s-app=%s", name),
	})

	require.NoError(t, err)
	assertPodsAreRunning(t, pods)
}

// Asserts that a deployment is running with the correct number of replicas.
func assertDeploymentIsRunning(t *testing.T, cs *framework.ClientSet, name string) {
	t.Helper()

	dp, err := cs.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	require.NoError(t, err)

	number := dp.Status.Replicas

	numbers := map[string]int32{
		"replicas":          dp.Status.Replicas,
		"readyReplicas":     dp.Status.Replicas,
		"availableReplicas": dp.Status.AvailableReplicas,
	}

	for name, value := range numbers {
		assert.Equal(t, value, number, "expected %s to equal %d, got: %s", name, number, value)
	}

	pods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("k8s-app=%s", name),
	})

	require.NoError(t, err)
	assertPodsAreRunning(t, pods)
}

// Asserts that a given list of pods are all running.
func assertPodsAreRunning(t *testing.T, pods *corev1.PodList) {
	t.Helper()

	for _, pod := range pods.Items {
		pod := pod
		t.Run(pod.Name, func(t *testing.T) {
			assertPodStatus(t, &pod)
		})
	}
}

// Asserts that a given pods' container status is as expected.
func assertPodStatus(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	assert.Equal(t, pod.Status.Phase, corev1.PodRunning)

	for _, containerStatus := range pod.Status.ContainerStatuses {
		assert.True(t, containerStatus.Ready)
		assert.NotNil(t, containerStatus.Started)
		assert.True(t, *containerStatus.Started)
		assert.NotNil(t, containerStatus.State.Running)
	}
}

// Asserts that the MCO Cluster Operator is showing as ready.
func testClusterOperator(t *testing.T, cs *framework.ClientSet) {
	co, err := cs.ConfigV1Interface.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
	require.NoError(t, err)

	isTechPreview, err := isTechPreviewEnabled(cs)
	require.NoError(t, err)

	assertClusterOperator(t, co, isTechPreview)
}

func testClusterOperators(t *testing.T, cs *framework.ClientSet) {
	co, err := cs.ConfigV1Interface.ClusterOperators().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	isTechPreview, err := isTechPreviewEnabled(cs)
	require.NoError(t, err)

	for _, op := range co.Items {
		if op.Name == "machine-config" {
			t.Run(op.Name, func(t *testing.T) {
				assertClusterOperator(t, &op, isTechPreview)
			})
		}
	}
}

// Asserts that a cluster operator is in the expected ready state.
func assertClusterOperator(t *testing.T, op *configv1.ClusterOperator, isTechPreview bool) {
	t.Helper()

	expected := map[configv1.ClusterStatusConditionType]configv1.ConditionStatus{
		configv1.OperatorAvailable:   configv1.ConditionTrue,
		configv1.OperatorDegraded:    configv1.ConditionFalse,
		configv1.OperatorProgressing: configv1.ConditionFalse,
		configv1.OperatorUpgradeable: configv1.ConditionTrue,
	}

	for _, condition := range op.Status.Conditions {
		if condition.Type == configv1.EvaluationConditionsDetected {
			assert.True(t, condition.Status == configv1.ConditionFalse || condition.Status == configv1.ConditionUnknown)
			continue
		}

		expectedStatus, ok := expected[condition.Type]
		if !ok {
			t.Logf("Cluster operator %s has condition type %s, ignoring", op.Name, condition.Type)
			continue
		}

		if isTechPreview && isClusterOperatorConditionFalseBecauseOfTechPreview(condition) {
			t.Logf("Cluster operator %s is not upgradeable because %s is enabled", op.Name, configv1.TechPreviewNoUpgrade)
			continue
		}

		assert.Equal(t, expectedStatus, condition.Status, "expected condition %s to be %s: got %s", condition.Type, expectedStatus, condition.Status)
	}
}

// Checks whether the cluster operator status condition is false because tech preview is enabled.
func isClusterOperatorConditionFalseBecauseOfTechPreview(c configv1.ClusterOperatorStatusCondition) bool {
	if c.Type != configv1.OperatorUpgradeable {
		return false
	}

	if c.Status != configv1.ConditionFalse {
		return false
	}

	if !strings.Contains(c.Message, string(configv1.TechPreviewNoUpgrade)) {
		return false
	}

	if !strings.Contains(c.Reason, string(configv1.TechPreviewNoUpgrade)) {
		return false
	}

	return true
}

// Checks whether tech preview is enabled on the test cluster.
func isTechPreviewEnabled(cs *framework.ClientSet) (bool, error) {
	fg, err := cs.ConfigV1Interface.FeatureGates().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	return fg.Spec.FeatureSet == configv1.TechPreviewNoUpgrade, nil
}

// Checks whether the OnClusterBuild featuregate is enabled on the test cluster.
func isOnClusterBuildEnabled(cs *framework.ClientSet) (bool, error) {
	fg, err := cs.ConfigV1Interface.FeatureGates().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	if fg.Spec.FeatureSet != configv1.TechPreviewNoUpgrade {
		return false, nil
	}

	if !isFeatureGateEnabled(fg, "OnClusterBuild") {
		return false, nil
	}

	moscList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	return len(moscList.Items) != 0, nil
}

// Checks that a given featuregate is enabled.
func isFeatureGateEnabled(fg *configv1.FeatureGate, name configv1.FeatureGateName) bool {
	for _, details := range fg.Status.FeatureGates {
		for _, disabled := range details.Disabled {
			if disabled.Name == name {
				return false
			}
		}

		for _, enabled := range details.Enabled {
			if enabled.Name == name {
				return true
			}
		}
	}

	return false
}
