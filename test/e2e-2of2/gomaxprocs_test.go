package e2e_2of2_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSystemGomaxprocsBehavior(t *testing.T) {
	cs := framework.NewClientSet("")
	if !gomaxprocsInjectionEnabled(t, cs) {
		t.Skip("GomaxprocsInjection feature gate is not enabled")
	}

	poolName := "node-gomaxprocs"
	node := helpers.GetRandomNode(t, cs, "worker")
	t.Cleanup(helpers.CreatePoolWithNode(t, cs, poolName, node))
	defaultConfig := helpers.GetMcName(t, cs, poolName)

	kubeletConfig := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "system-gomaxprocs"},
		Spec: mcfgv1.KubeletConfigSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"pools.operator.machineconfiguration.openshift.io/" + poolName: "",
			}},
			SystemGomaxprocsBehavior: mcfgv1.GomaxprocsBehaviorAutosize,
		},
	}
	_, err := cs.KubeletConfigs().Create(context.Background(), kubeletConfig, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		err := cs.KubeletConfigs().Delete(context.Background(), kubeletConfig.Name, metav1.DeleteOptions{})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
		}
		require.NoError(t, helpers.WaitForPoolComplete(t, cs, poolName, defaultConfig))
	})

	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, fmt.Sprintf("99-%s-generated-kubelet", poolName))

	nodeSizing := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/etc/node-sizing-enabled.env")
	require.Contains(t, string(nodeSizing), "NODE_SIZING_ENABLED=true")

	for _, service := range []string{"kubelet.service", "crio.service"} {
		dropIn := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs/etc/systemd/system", service+".d/30-gomaxprocs.conf"))
		require.Contains(t, string(dropIn), "EnvironmentFile=-/run/system-gomaxprocs.env")

		gomaxprocs := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "sh", "-c", fmt.Sprintf(`tr '\000' '\n' < /proc/$(systemctl show -p MainPID --value %s)/environ | grep -m1 '^GOMAXPROCS='`, service))
		require.Regexp(t, `GOMAXPROCS=[1-9][0-9]*`, string(gomaxprocs))
	}
}

func gomaxprocsInjectionEnabled(t *testing.T, cs *framework.ClientSet) bool {
	t.Helper()

	featureGate, err := cs.FeatureGates().Get(context.Background(), "cluster", metav1.GetOptions{})
	require.NoError(t, err)
	for _, details := range featureGate.Status.FeatureGates {
		for _, enabled := range details.Enabled {
			if enabled.Name == "GomaxprocsInjection" {
				return true
			}
		}
	}
	return false
}
