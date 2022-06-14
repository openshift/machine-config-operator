package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestKdumpEnablement(t *testing.T) {
	cs := framework.NewClientSet("")

	// Create infra pool to roll out MC changes
	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	helpers.CreateMCP(t, cs, "infra")

	// create old mc to have something to verify we successfully rolled back
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")
	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	// create kargs MC
	kdumpEnabled := true
	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("99-worker-kdump-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(&ign3types.Config{
					Ignition: ign3types.Ignition{
						Version: ign3types.MaxVersion.String(),
					},
					Systemd: ign3types.Systemd{
						Units: []ign3types.Unit{
							{
								Name:    "kdump.service",
								Enabled: &kdumpEnabled,
							},
						},
					},
				}),
			},
			KernelArguments: []string{"crashkernel=300M"},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), kargsMC, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", kargsMC.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", kargsMC.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	// Re-fetch the infra node for updated annotations
	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")
	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kargs := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/proc/cmdline")

	if !strings.Contains(kargs, "crashkernel=300M") {
		t.Fatalf("Missing %q in kargs: %q", "crashkernel=300M", kargs)
	}
	t.Logf("Node %s has expected craskkernel karg", infraNode.Name)

	helpers.ExecCmdOnNode(t, cs, infraNode, "/bin/sh", "-c", string("chroot /rootfs systemctl reboot"))
	// Waiting for the node to come back up after reboot
	time.Sleep(time.Minute)

	kdumpServiceStarted := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/sys/kernel/kexec_crash_loaded")
	if !strings.Contains(kdumpServiceStarted, "1") {
		t.Fatalf("kdump.service failed to start on %s", infraNode.Name)
	}
	t.Logf("Node %s has active kdump.service", infraNode.Name)

	helpers.ExecCmdOnNode(t, cs, infraNode, "/bin/sh", "-c", string("echo 1 > /rootfs/proc/sys/kernel/sysrq"))
	t.Logf("Triggering sysrq on Node %s", infraNode.Name)
	// This will trigger kdump, which will write the kernel core, then reboot.
	helpers.ExecCmdOnNode(t, cs, infraNode, "/bin/sh", "-c", string("echo c > /rootfs/proc/sysrq-trigger"))

	// Waiting for the node to come back up after kernel crash
	time.Sleep(time.Minute)
	kcore := helpers.ExecCmdOnNode(t, cs, infraNode, "ls", "/rootfs/var/crash/")
	if kcore == "" {
		t.Fatalf("No kcore found in /var/crash")
	}
	t.Logf("Node %s has kernel crash dump named %s in /var/crash", infraNode.Name, kcore)

	// cleanup - delete karg mc and rollback
	if err := cs.MachineConfigs().Delete(context.TODO(), kargsMC.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}
	t.Logf("Deleted MachineConfig %s", kargsMC.Name)
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

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
