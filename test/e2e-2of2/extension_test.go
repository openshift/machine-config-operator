package e2e_2of2_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	constants "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

func TestExtensions(t *testing.T) {
	cs := framework.NewClientSet("")

	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), oldInfraConfig.Name, metav1.DeleteOptions{}))

	})

	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
	require.Nil(t, err)
	// Apply extensions
	extensions := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("extensions-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			Extensions: []string{"two-node-ha", "wasm", "ipsec", "usbguard", "kerberos", "kernel-devel", "sandboxed-containers", "sysstat"},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), extensions, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", extensions.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", extensions.Name)
	require.Nil(t, err)
	if err := helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig); err != nil {
		t.Fatal(err)
	}
	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	isOKD, err := helpers.IsOKDCluster(cs)
	require.Nil(t, err)

	var installedPackages string
	var expectedPackages []string
	if isOKD {
		// OKD does not support grouped extensions yet, so installing kernel-devel will not also pull in kernel-headers
		// "sandboxed-containers" extension is not available on OKD
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "pacemaker", "pcs", "fence-agents-all", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "sysstat")
		// "kerberos" extension is not available on OKD
		expectedPackages = []string{"libreswan", "usbguard", "kernel-devel"}
	} else {
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "pacemaker", "pcs", "fence-agents-all", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5", "sysstat")
		expectedPackages = []string{"pacemaker", "pcs", "fence-agents-all", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5", "sysstat"}

	}
	for _, v := range expectedPackages {
		if !strings.Contains(installedPackages, v) {
			t.Fatalf("Node %s doesn't have expected extensions", infraNode.Name)
		}
	}

	t.Logf("Node %s has expected extensions installed", infraNode.Name)

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), extensions.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", extensions.Name)

	// Wait for the mcp to rollback to previous config
	if err := helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	if isOKD {
		// OKD does not support grouped extensions yet, so installing kernel-devel will not also pull in kernel-headers
		// "sandboxed-containers" extension is not available on OKD
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-qa", "usbguard", "kernel-devel")
	} else {
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-qa", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5")
	}
	for _, v := range expectedPackages {
		if strings.Contains(installedPackages, v) {
			t.Fatalf("Node %s did not rollback successfully", infraNode.Name)
		}
	}

	t.Logf("Node %s has successfully rolled back", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)
}
