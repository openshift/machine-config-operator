package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/pkg/sysregistriesv2"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"
	mcoac "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	constants "github.com/openshift/machine-config-operator/pkg/daemon/constants"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// The MachineConfigPools to create for the tests.
	testMCPFileName    string = "infra-file"
	testMCPSSHName     string = "infra-ssh"
	testMCPUnitName    string = "infra-unit"
	testMCPSpecialName string = "infra-special"
)

func TestNodeDisruptionPolicies(t *testing.T) {

	cs := framework.NewClientSet("")
	nodes, err := helpers.GetNodesByRole(cs, "worker")
	require.Nil(t, err, "error grabbing nodes from worker pool")
	require.Greater(t, len(nodes), 0, "this test requires atleast one node in the worker pool")

	// By equally(based on run-time) separating out the actions to be tested across three parallel sub-tests,
	// the test suite can be run much faster. This still provides sufficient coverage as the calculation and
	// execution of policies are independant of each other. Each testFunc can handle any combination of testActions.

	testActionsAlpha := []opv1.NodeDisruptionPolicySpecAction{
		{Type: opv1.NoneSpecAction}, {Type: opv1.DrainSpecAction}}

	testActionsBeta := []opv1.NodeDisruptionPolicySpecAction{{Type: opv1.RebootSpecAction}}

	testActionsGamma := []opv1.NodeDisruptionPolicySpecAction{{Type: opv1.DaemonReloadSpecAction},
		{Type: opv1.RestartSpecAction, Restart: &opv1.RestartService{ServiceName: "crio.service"}},
		{Type: opv1.ReloadSpecAction, Reload: &opv1.ReloadService{ServiceName: "crio.service"}}}

	// Shuffle the three action sets so each testFunc is randomly assigned one of the above action sets.
	testActionSets := [][]opv1.NodeDisruptionPolicySpecAction{testActionsAlpha, testActionsBeta, testActionsGamma}
	helpers.ShuffleSlice(testActionSets)

	testFuncs := []func(*testing.T, corev1.Node, []opv1.NodeDisruptionPolicySpecAction){testFilePolicy, testUnitPolicy, testSSHKeyPolicy}

	for testIndex, testFunc := range testFuncs {
		testIndex := testIndex
		testFunc := testFunc
		t.Run(helpers.GetFunctionName(testFunc), func(t *testing.T) {
			// Only parallelize if there are enough nodes to run the tests individually
			if len(nodes) >= len(testFuncs) {
				t.Parallel()
				testFunc(t, nodes[testIndex], testActionSets[testIndex])
			} else {
				testFunc(t, nodes[0], testActionSets[testIndex])
			}

		})
	}

}

func testFilePolicy(t *testing.T, node corev1.Node, testActions []opv1.NodeDisruptionPolicySpecAction) {

	cs := framework.NewClientSet("")
	t.Cleanup(helpers.CreatePoolAndApplyMCToNode(t, cs, testMCPFileName, node, nil))

	// Step through each action and check if it was applied correctly. This loop generates a MachineConfiguration
	// and a MachineConfig for each testAction and verifies that the correct NodeDisruptionPolicy was applied.
	for _, action := range testActions {
		fileName := "/etc/test-" + string(action.Type)
		fileApplyConfiguration := mcoac.NodeDisruptionPolicySpecFile().WithPath(fileName).WithActions(helpers.GetActionApplyConfiguration(action))
		applyConfiguration := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithNodeDisruptionPolicy(mcoac.NodeDisruptionPolicyConfig().WithFiles(fileApplyConfiguration)))
		// Create the test MC object, derived from the action under test
		testMC := helpers.NewMachineConfig("01-test-file", helpers.MCLabelForRole(testMCPFileName), "", []ign3types.File{ctrlcommon.NewIgnFile(fileName, "test\n")})
		checkNodeDisruptionAction(t, cs, testMC, testMCPFileName, *applyConfiguration, node, action.Type)
	}

}

func testUnitPolicy(t *testing.T, nodeUnderTest corev1.Node, testActions []opv1.NodeDisruptionPolicySpecAction) {

	cs := framework.NewClientSet("")
	t.Cleanup(helpers.CreatePoolAndApplyMCToNode(t, cs, testMCPUnitName, nodeUnderTest, nil))

	// Step through each action and check if it was applied correctly. This loop generates a MachineConfiguration
	// and a MachineConfig for each testAction and verifies that the correct NodeDisruptionPolicy was applied.
	for _, action := range testActions {
		serviceName := string(action.Type) + "-test.service"
		serviceApplyConfiguration := mcoac.NodeDisruptionPolicySpecUnit().WithName(opv1.NodeDisruptionPolicyServiceName(serviceName)).WithActions(helpers.GetActionApplyConfiguration(action))
		applyConfiguration := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithNodeDisruptionPolicy(mcoac.NodeDisruptionPolicyConfig().WithUnits(serviceApplyConfiguration)))
		testMC := helpers.NewMachineConfigExtended("01-test-unit", helpers.MCLabelForRole(testMCPUnitName), nil, nil, []ign3types.Unit{{Name: serviceName, Contents: helpers.StrToPtr("test")}}, nil, nil, false, nil, "", "")
		checkNodeDisruptionAction(t, cs, testMC, testMCPUnitName, *applyConfiguration, nodeUnderTest, action.Type)
	}
}

func testSSHKeyPolicy(t *testing.T, node corev1.Node, testActions []opv1.NodeDisruptionPolicySpecAction) {

	cs := framework.NewClientSet("")
	t.Cleanup(helpers.CreatePoolAndApplyMCToNode(t, cs, testMCPSSHName, node, nil))

	// Step through each action and check if it was applied correctly. This loop generates a MachineConfiguration
	// and a MachineConfig for each testAction and verifies that the correct NodeDisruptionPolicy was applied.
	for _, action := range testActions {
		sshApplyConfiguration := mcoac.NodeDisruptionPolicySpecSSHKey().WithActions(helpers.GetActionApplyConfiguration(action))
		applyConfiguration := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithNodeDisruptionPolicy(mcoac.NodeDisruptionPolicyConfig().WithSSHKey(sshApplyConfiguration)))
		testMC := helpers.NewMachineConfigExtended("01-test-ssh", helpers.MCLabelForRole(testMCPSSHName), nil, nil, nil, []ign3types.SSHAuthorizedKey{"test"}, nil, false, nil, "", "")
		checkNodeDisruptionAction(t, cs, testMC, testMCPSSHName, *applyConfiguration, node, action.Type)
	}

}

// This is a bespoke test function for the Special Action. This verifies that drain only takes
// place when registries are removed from /etc/containers/registries.conf and no drain takes
// place when registried are added. It also verifies that crio reloads takes place in both cases.
func TestNodeDisruptionPolicySpecialAction(t *testing.T) {

	t.Logf("Verifying Special action")

	cs := framework.NewClientSet("")
	t.Cleanup(helpers.CreatePoolAndApplyMC(t, cs, testMCPSpecialName, nil))
	nodeUnderTest := helpers.GetSingleNodeByRole(t, cs, testMCPSpecialName)

	// Grab name of MCD pod under test
	mcdPod, err := helpers.MCDForNode(cs, &nodeUnderTest)
	require.Nil(t, err, "determing mcd pod of node under test failed")

	// Grab old rendered MC to figure out transition back
	oldRenderedMC := helpers.GetMcName(t, cs, testMCPSpecialName)

	// Modify registry file on disk by adding a new "test.io" domain
	tomlReg := sysregistriesv2.V2RegistriesConf{}
	_, err = toml.Decode(helpers.GetFileContentOnNode(t, cs, nodeUnderTest, constants.ContainerRegistryConfPath), &tomlReg)
	require.Nil(t, err, "failed decoding TOML content from file %s: %w", constants.ContainerRegistryConfPath, err)
	tomlReg.UnqualifiedSearchRegistries = append(tomlReg.UnqualifiedSearchRegistries, "test.io")
	var newFile bytes.Buffer
	encoder := toml.NewEncoder(&newFile)
	err = encoder.Encode(tomlReg)
	require.Nil(t, err, "failed encoding TOML content into file %s: %w", constants.ContainerRegistryConfPath, err)

	// Create the a test machineconfig from the file in cluster
	testMC := helpers.NewMachineConfig("01-test", helpers.MCLabelForRole(testMCPSpecialName), "", []ign3types.File{ctrlcommon.NewIgnFile(constants.ContainerRegistryConfPath, newFile.String())})
	_, err = cs.MachineconfigurationV1Interface.MachineConfigs().Create(context.TODO(), testMC, metav1.CreateOptions{})
	require.Nil(t, err, "creating test machine config failed")

	// Wait for transition to new config
	renderedMC := helpers.WaitForConfigAndPoolComplete(t, cs, testMCPSpecialName, testMC.Name)

	// Check daemon logs to ensure drain was not executed for this case and that a crio reload took place for this MC change.
	helpers.AssertMCDLogsContain(t, cs, mcdPod, &nodeUnderTest, fmt.Sprintf("Drain calculated for node disruption: false for config %s", renderedMC))
	helpers.AssertMCDLogsContain(t, cs, mcdPod, &nodeUnderTest, fmt.Sprintf("Performing post config change action: Special for config %s", renderedMC))

	// Remove test config, this is the equivalent of deleting a registry
	cs.MachineconfigurationV1Interface.MachineConfigs().Delete(context.TODO(), testMC.Name, metav1.DeleteOptions{})
	require.Nil(t, err, "deleting test machine config failed")

	// Wait for pool to return to old MC before checking actions
	helpers.WaitForPoolComplete(t, cs, testMCPSpecialName, oldRenderedMC)

	// Check daemon logs to ensure drain action was executed and that a crio reload took place for this MC change.
	helpers.AssertMCDLogsContain(t, cs, mcdPod, &nodeUnderTest, fmt.Sprintf("Drain calculated for node disruption: true for config %s", oldRenderedMC))
	helpers.AssertMCDLogsContain(t, cs, mcdPod, &nodeUnderTest, fmt.Sprintf("Performing post config change action: Special for config %s", oldRenderedMC))

	t.Logf("Successfully verified Special action")

}

func checkNodeDisruptionAction(t *testing.T, cs *framework.ClientSet, testMC *v1.MachineConfig, testMCP string, applyConfiguration mcoac.MachineConfigurationApplyConfiguration, nodeUnderTest corev1.Node, expectedAction opv1.NodeDisruptionPolicySpecActionType) {

	t.Logf("Verifying %s", expectedAction)

	// Grab the client
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())

	// This may be needed to verify reboot action
	initialUptime := helpers.GetNodeUptime(t, cs, nodeUnderTest)

	// Grab name of MCD pod under test
	mcdPod, err := helpers.MCDForNode(cs, &nodeUnderTest)
	require.Nil(t, err, "determing mcd pod of node under test failed")

	// Apply the node disruption policy
	_, err = machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), &applyConfiguration, metav1.ApplyOptions{FieldManager: "machine-config-operator" + testMCP})
	require.Nil(t, err, "updating cluster node disruption policy failed")

	// Grab old rendered MC to figure out transition back
	oldRenderedMC := helpers.GetMcName(t, cs, testMCP)

	// Create the test machineconfig in cluster
	_, err = cs.MachineconfigurationV1Interface.MachineConfigs().Create(context.TODO(), testMC, metav1.CreateOptions{})
	require.Nil(t, err, "creating test machine config failed")

	// Wait for transition to new config
	renderedMC := helpers.WaitForConfigAndPoolComplete(t, cs, testMCP, testMC.Name)

	// Check daemon logs to ensure correct action was executed
	if expectedAction == opv1.DrainSpecAction {
		helpers.AssertMCDLogsContain(t, cs, mcdPod, &nodeUnderTest, fmt.Sprintf("Drain calculated for node disruption: true for config %s", renderedMC))
	} else if expectedAction == opv1.RebootSpecAction {
		helpers.AssertNodeReboot(t, cs, nodeUnderTest, initialUptime)
	} else {
		helpers.AssertMCDLogsContain(t, cs, mcdPod, &nodeUnderTest, fmt.Sprintf("Performing post config change action: %v for config %s", expectedAction, renderedMC))
	}

	// Remove test config
	cs.MachineconfigurationV1Interface.MachineConfigs().Delete(context.TODO(), testMC.Name, metav1.DeleteOptions{})
	require.Nil(t, err, "deleting test machine config failed")

	// Wait for pool to return to old MC before running next test case
	helpers.WaitForPoolComplete(t, cs, testMCP, oldRenderedMC)

	t.Logf("Successfully verified %s", expectedAction)
}
