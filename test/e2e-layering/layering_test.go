package e2e_layering_test

import (
	"testing"

	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
)

type LayeringSuite struct {
	suite.Suite
}

func (suite *LayeringSuite) SetupSuite() {
	cs := framework.NewClientSet("")

	// don't keep unlabel function or delete pool function since removing a node from a layered pool is...not well defined
	helpers.LabelAllNodesInPool(suite.T(), cs, "worker", "node-role.kubernetes.io/"+ctrlcommon.ExperimentalLayeringPoolName)
	helpers.CreateMCP(suite.T(), cs, ctrlcommon.ExperimentalLayeringPoolName)

	helpers.WaitForLayeredPoolInitialUpdate(suite.T(), cs, ctrlcommon.ExperimentalLayeringPoolName)
}

func (suite *LayeringSuite) TestNoReboot() {
	cs := framework.NewClientSet("")
	unlabelFunc := helpers.LabelRandomNodeFromPool(suite.T(), cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraRenderedConfig := helpers.GetMcName(suite.T(), cs, "infra")

	infraNode := helpers.GetSingleNodeByRole(suite.T(), cs, "infra")

	output := helpers.ExecCmdOnNode(suite.T(), cs, infraNode, "cat", "/rootfs/proc/uptime")
	oldTime := strings.Split(output, " ")[0]
	suite.T().Logf("Node %s initial uptime: %s", infraNode.Name, oldTime)

	// Adding authorized key for user core
	testIgnConfig := ctrlcommon.NewIgnConfig()
	testSSHKey := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"test adding authorized key without node reboot"}}
	testIgnConfig.Passwd.Users = append(testIgnConfig.Passwd.Users, testSSHKey)

	addAuthorizedKey := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("authorzied-key-infra-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(testIgnConfig),
			},
		},
	}

	_, err := cs.MachineConfigs().Create(context.TODO(), addAuthorizedKey, metav1.CreateOptions{})
	require.Nil(suite.T(), err, "failed to create MC")
	suite.T().Logf("Created %s", addAuthorizedKey.Name)

	// grab the latest worker- MC
	renderedConfig, err := helpers.WaitForRenderedConfig(suite.T(), cs, "infra", addAuthorizedKey.Name)
	require.Nil(suite.T(), err)
	err = helpers.WaitForPoolComplete(suite.T(), cs, "infra", renderedConfig)
	require.Nil(suite.T(), err)

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(suite.T(), cs, "infra")

	// TODO come up with equivalent layering condition
	// assert.Equal(suite.T(), infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(suite.T(), infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	foundSSHKey := helpers.ExecCmdOnNode(suite.T(), cs, infraNode, "cat", "/rootfs/home/core/.ssh/authorized_keys")
	if !strings.Contains(foundSSHKey, "test adding authorized key without node reboot") {
		suite.T().Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSHKey)
	}
	suite.T().Logf("Node %s has SSH key", infraNode.Name)

	usernameAndGroup := strings.Split(strings.TrimSuffix(helpers.ExecCmdOnNode(suite.T(), cs, infraNode, "chroot", "/rootfs", "stat", "--format=%U %G", "/home/core/.ssh/authorized_keys"), "\n"), " ")
	assert.Equal(suite.T(), usernameAndGroup, []string{constants.CoreUserName, constants.CoreGroupName})

	output = helpers.ExecCmdOnNode(suite.T(), cs, infraNode, "cat", "/rootfs/proc/uptime")
	newTime := strings.Split(output, " ")[0]

	// To ensure we didn't reboot, new uptime should be greater than old uptime
	uptimeOld, err := strconv.ParseFloat(oldTime, 64)
	require.Nil(suite.T(), err)

	uptimeNew, err := strconv.ParseFloat(newTime, 64)
	require.Nil(suite.T(), err)

	if uptimeOld > uptimeNew {
		suite.T().Fatalf("Node %s rebooted uptime decreased from %f to %f", infraNode.Name, uptimeOld, uptimeNew)
	}

	suite.T().Logf("Node %s didn't reboot as expected, uptime increased from %f to %f ", infraNode.Name, uptimeOld, uptimeNew)

	// Delete the applied authorized key MachineConfig to make sure rollback works fine without node reboot
	if err := cs.MachineConfigs().Delete(context.TODO(), addAuthorizedKey.Name, metav1.DeleteOptions{}); err != nil {
		suite.T().Error(err)
	}

	suite.T().Logf("Deleted MachineConfig %s", addAuthorizedKey.Name)

	// Wait for the mcp to rollback to previous config
	if err := helpers.WaitForPoolComplete(suite.T(), cs, "infra", oldInfraRenderedConfig); err != nil {
		suite.T().Fatal(err)
	}

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(suite.T(), cs, "infra")

	assert.Equal(suite.T(), infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(suite.T(), infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	foundSSHKey = helpers.ExecCmdOnNode(suite.T(), cs, infraNode, "cat", "/rootfs/home/core/.ssh/authorized_keys")
	if strings.Contains(foundSSHKey, "test adding authorized key without node reboot") {
		suite.T().Fatalf("Node %s did not rollback successfully", infraNode.Name)
	}

	suite.T().Logf("Node %s has successfully rolled back", infraNode.Name)

	// Ensure that node didn't reboot during rollback
	output = helpers.ExecCmdOnNode(suite.T(), cs, infraNode, "cat", "/rootfs/proc/uptime")
	newTime = strings.Split(output, " ")[0]

	uptimeNew, err = strconv.ParseFloat(newTime, 64)
	require.Nil(suite.T(), err)

	if uptimeOld > uptimeNew {
		suite.T().Fatalf("Node %s rebooted during rollback, uptime decreased from %f to %f", infraNode.Name, uptimeOld, uptimeNew)
	}

	suite.T().Logf("Node %s didn't reboot as expected during rollback, uptime increased from %f to %f ", infraNode.Name, uptimeOld, uptimeNew)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(suite.T(), err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(suite.T(), err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		suite.T().Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(suite.T(), cs, "infra", oldInfraRenderedConfig)
	require.Nil(suite.T(), err)
}

func TestLayeringSuite(t *testing.T) {
	suite.Run(t, new(LayeringSuite))
}
