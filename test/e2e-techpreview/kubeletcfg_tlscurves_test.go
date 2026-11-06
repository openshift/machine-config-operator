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
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
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
	curveIDSecP256r1      = 23
	curveIDSecP384r1      = 24
	curveIDX25519         = 29
	curveIDX25519MLKEM768 = 4588
)

// TestKubeletConfigTLSCurvePreferencesNamedProfile verifies that named profiles, including the Intermediate fallback, contribute groups when
// TLSGroupPreferences is enabled. FIPS targets keep only the approved groups.
func TestKubeletConfigTLSCurvePreferencesNamedProfile(t *testing.T) {
	cs := framework.NewClientSet("")
	requireTLSGroupPreferencesEnabled(t, cs)

	apiServer, err := cs.ConfigV1Interface.APIServers().Get(context.TODO(), ctrlcommon.APIServerInstanceName, metav1.GetOptions{})
	require.NoError(t, err, "failed to read the cluster APIServer")
	if p := apiServer.Spec.TLSSecurityProfile; p != nil && p.Type == configv1.TLSProfileCustomType {
		t.Skip("cluster APIServer uses a Custom profile, so the named-profile baseline does not apply")
	}

	for _, role := range []string{"worker", "master"} {
		t.Run(role, func(t *testing.T) {
			node := helpers.GetRandomNode(t, cs, role)
			mcName := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
			require.NotEmpty(t, mcName, "node %s must have a current MachineConfig", node.Name)
			mc, err := cs.MachineConfigs().Get(context.TODO(), mcName, metav1.GetOptions{})
			require.NoError(t, err)
			want := []int32{curveIDX25519MLKEM768, curveIDX25519, curveIDSecP256r1, curveIDSecP384r1}
			if mc.Spec.FIPS {
				want = []int32{curveIDSecP256r1, curveIDSecP384r1}
			}

			raw, parsed := readKubeletConfig(t, cs, node)
			require.Contains(t, raw, "tlsCurvePreferences", "node %s must render named-profile groups", node.Name)
			require.Equal(t, want, parsed.TLSCurvePreferences, "node %s", node.Name)
		})
	}
}

// Check a Custom group override on one node and restore the baseline after deletion.
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
	_, baseline := readKubeletConfig(t, cs, node)

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

	t.Run("deleting the KubeletConfig restores the baseline", func(t *testing.T) {
		require.NoError(t, cs.KubeletConfigs().Delete(context.TODO(), kcName, metav1.DeleteOptions{}))
		kcDeleted = true
		require.NoError(t, helpers.WaitForPoolComplete(t, cs, poolName, defaultTarget))

		raw, parsed := readKubeletConfig(t, cs, node)
		require.Equal(t, baseline.TLSCurvePreferences, parsed.TLSCurvePreferences,
			"kubelet.conf on %s should restore the original groups once the KubeletConfig is gone", node.Name)
		if len(baseline.TLSCurvePreferences) == 0 {
			require.NotContains(t, raw, "tlsCurvePreferences", "an empty baseline must omit the key")
		}
	})
}

// requireTLSGroupPreferencesEnabled skips clusters where groups are unavailable.
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

// readKubeletConfig returns raw text for key-presence checks and parsed field values.
func readKubeletConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node) (string, kubeletconfigv1beta1.KubeletConfiguration) {
	t.Helper()

	raw := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", kubeletConfPath))
	var parsed kubeletconfigv1beta1.KubeletConfiguration
	require.NoError(t, yaml.Unmarshal([]byte(raw), &parsed), "failed to parse kubelet.conf from node %s", node.Name)
	return raw, parsed
}

// machineConfigForKubeletConfig waits for the CR's generated MachineConfig.
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
