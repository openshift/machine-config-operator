package e2e_techpreview

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"sigs.k8s.io/yaml"
)

const kubeletConfPath = "/etc/kubernetes/kubelet.conf"

const (
	curveIDSecP256r1 = 23
	curveIDSecP384r1 = 24
)

// TestKubeletConfigTLSCurvePreferencesAbsentByDefault ensures tlsCurvePreferences
// is rendered only when explicitly configured by the Custom profile.
func TestKubeletConfigTLSCurvePreferencesAbsentByDefault(t *testing.T) {
	cs := framework.NewClientSet("")

	// A cluster that genuinely configures groups legitimately renders the key,
	// which would make the assertion below a false failure rather than a real one.
	apiServer, err := cs.ConfigV1Interface.APIServers().Get(context.TODO(), ctrlcommon.APIServerInstanceName, metav1.GetOptions{})
	require.NoError(t, err, "failed to read the cluster APIServer")
	if p := apiServer.Spec.TLSSecurityProfile; p != nil && p.Type == configv1.TLSProfileCustomType &&
		p.Custom != nil && len(p.Custom.Groups) > 0 {
		t.Skip("cluster APIServer configures tlsSecurityProfile.custom.groups, so the unconfigured baseline does not apply")
	}

	for _, role := range []string{"worker", "master"} {
		t.Run(role, func(t *testing.T) {
			node := helpers.GetRandomNode(t, cs, role)
			raw, parsed := readKubeletConfig(t, cs, node)
			require.NotContains(t, raw, "tlsCurvePreferences",
				"kubelet.conf on %s must omit the key entirely, not render an empty list", node.Name)
			require.Nil(t, parsed.TLSCurvePreferences)
		})
	}
}

// TestKubeletConfigTLSCurvePreferences applies a KubeletConfig carrying TLS groups
// to a single-node pool and checks the names are translated to IANA codepoints on
// disk, that the kubelet accepts them, and that deleting the CR removes the key.
func TestKubeletConfigTLSCurvePreferences(t *testing.T) {
	cs := framework.NewClientSet("")
	requireTLSGroupPreferencesEnabled(t, cs)

	const (
		testName = "tls-curves"
		kcName   = "kubelet-test-tls-curves"
	)
	poolName := fmt.Sprintf("node-%s", testName)
	mcName := fmt.Sprintf("mc-%s", testName)

	cleanupFuncs := make([]func(), 0)
	defer func() {
		for _, f := range cleanupFuncs {
			f()
		}
	}()

	cleanupFuncs = append(cleanupFuncs, helpers.LabelRandomNodeFromPool(t, cs, "worker", helpers.MCPNameToRole(poolName)))
	cleanupFuncs = append(cleanupFuncs, func() {
		// the sleep allows the unlabelling to take effect
		time.Sleep(time.Second * 5)
		if err := helpers.WaitForPoolComplete(t, cs, "worker", helpers.GetMcName(t, cs, "worker")); err != nil {
			t.Logf("failed to wait for pool %v", err)
		}
	})
	cleanupFuncs = append(cleanupFuncs, helpers.CreateMCP(t, cs, poolName))

	node := helpers.GetSingleNodeByRole(t, cs, poolName)
	raw, _ := readKubeletConfig(t, cs, node)
	require.NotContains(t, raw, "tlsCurvePreferences",
		"node %s already renders curves before the test configures any", node.Name)

	// create default mc to have something to verify we successfully rolled back
	defaultMC := helpers.CreateMC(mcName, poolName)
	_, err := cs.MachineConfigs().Create(context.TODO(), defaultMC, metav1.CreateOptions{})
	require.NoError(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		if err := cs.MachineConfigs().Delete(context.TODO(), defaultMC.Name, metav1.DeleteOptions{}); err != nil {
			t.Logf("failed to delete MachineConfig %s: %v", defaultMC.Name, err)
		}
	})
	defaultTarget := helpers.WaitForConfigAndPoolComplete(t, cs, poolName, defaultMC.Name)

	kc := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: kcName},
		Spec: mcfgv1.KubeletConfigSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{poolName: ""},
			},
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS13,
						Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
						Groups: []configv1.TLSGroup{
							configv1.TLSGroupSecP384r1,
							configv1.TLSGroupSecP256r1,
						},
					},
				},
			},
		},
	}
	_, err = cs.KubeletConfigs().Create(context.TODO(), kc, metav1.CreateOptions{})
	require.NoError(t, err)
	kcDeleted := false
	cleanupFuncs = append(cleanupFuncs, func() {
		if kcDeleted {
			return
		}
		if err := cs.KubeletConfigs().Delete(context.TODO(), kcName, metav1.DeleteOptions{}); err != nil {
			t.Logf("failed to delete KubeletConfig %s: %v", kcName, err)
		}
	})

	kcMCName, err := machineConfigForKubeletConfig(cs, kcName)
	require.NoError(t, err, "failed to find the machine config rendered from the kubelet config")
	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, kcMCName)

	t.Run("groups are translated to codepoints on disk", func(t *testing.T) {
		_, parsed := readKubeletConfig(t, cs, node)
		require.Equal(t, []int32{curveIDSecP384r1, curveIDSecP256r1}, parsed.TLSCurvePreferences,
			"kubelet.conf on %s should carry the configured groups as IANA codepoints, in order", node.Name)
	})

	t.Run("kubelet accepts the rendered curves", func(t *testing.T) {
		out := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "systemctl", "is-active", "kubelet")
		require.Contains(t, out, "active", "kubelet on %s should still be running", node.Name)
	})

	t.Run("deleting the KubeletConfig removes the key", func(t *testing.T) {
		require.NoError(t, cs.KubeletConfigs().Delete(context.TODO(), kcName, metav1.DeleteOptions{}))
		kcDeleted = true
		require.NoError(t, helpers.WaitForPoolComplete(t, cs, poolName, defaultTarget))

		raw, parsed := readKubeletConfig(t, cs, node)
		require.NotContains(t, raw, "tlsCurvePreferences",
			"kubelet.conf on %s should drop the key entirely once the KubeletConfig is gone", node.Name)
		require.Nil(t, parsed.TLSCurvePreferences)
	})
}

// requireTLSGroupPreferencesEnabled skips unless the gate is on. This suite is only
// meant to run under TechPreview, but without the guard a Default cluster fails on
// an assertion about codepoints rather than saying groups was pruned on write.
func requireTLSGroupPreferencesEnabled(t *testing.T, cs *framework.ClientSet) {
	t.Helper()

	fg, err := cs.ConfigV1Interface.FeatureGates().Get(context.TODO(), "cluster", metav1.GetOptions{})
	require.NoError(t, err, "failed to read the cluster FeatureGate")

	for _, version := range fg.Status.FeatureGates {
		for _, enabled := range version.Enabled {
			if enabled.Name == features.FeatureGateTLSGroupPreferences {
				return
			}
		}
	}
	t.Skipf("FeatureGate %s is not enabled on this cluster", features.FeatureGateTLSGroupPreferences)
}

// readKubeletConfig returns kubelet.conf from a node both raw and parsed. The raw
// form is needed to tell an absent key from an empty list; the parsed form gives an
// ordered []int32 to compare, which a regex over a YAML block list cannot.
func readKubeletConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node) (string, kubeletconfigv1beta1.KubeletConfiguration) {
	t.Helper()

	raw := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", kubeletConfPath))
	var parsed kubeletconfigv1beta1.KubeletConfiguration
	require.NoError(t, yaml.Unmarshal([]byte(raw), &parsed), "failed to parse kubelet.conf from node %s", node.Name)
	return raw, parsed
}

// machineConfigForKubeletConfig returns the MachineConfig the kubelet-config
// controller generated for the named KubeletConfig, waiting for it to appear.
func machineConfigForKubeletConfig(cs *framework.ClientSet, kcName string) (string, error) {
	var mcName string
	if err := wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcs, err := cs.MachineConfigs().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcs.Items {
			for _, ownerRef := range mc.GetOwnerReferences() {
				if ownerRef.Kind == "KubeletConfig" && ownerRef.Name == kcName {
					mcName = mc.Name
					return true, nil
				}
			}
		}
		return false, nil
	}); err != nil {
		return "", fmt.Errorf("can't find machine config created by kubelet config %s: %w", kcName, err)
	}
	return mcName, nil
}
