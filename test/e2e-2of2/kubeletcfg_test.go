package e2e_2of2_test

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	kcfg "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeletypes "k8s.io/kubernetes/pkg/kubelet/types"
)

const (
	kubeletPath = "/etc/kubernetes/kubelet.conf"
)

func TestKubeletConfigDefaultUpdateFreq(t *testing.T) {
	autoSizing := false
	resources := make(map[string]string)
	matchLabels := make(map[string]string)
	matchLabels["pools.operator.machineconfiguration.openshift.io/infra"] = ""
	resources["cpu"] = "100m"
	// this might get caught by the test below that says if default == current but that is ok since we want to make sure nodeStatusUpdateFrequency is 10s
	kcRaw1, err := kcfg.EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{}, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
	require.Nil(t, err, "failed to encode kubelet config")
	kc1 := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-100"},
		Spec: mcfgv1.KubeletConfigSpec{
			AutoSizingReserved: &autoSizing,
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw1,
			}, MachineConfigPoolSelector: &metav1.LabelSelector{MatchLabels: matchLabels},
		},
	}

	runTestWithKubeletCfg(t, "resources", []string{`"?nodeStatusUpdateFrequency"?: (\S+)`}, []string{"nodeStatusUpdateFrequency"}, [][]string{{"10s"}}, kc1, nil)
}
func TestKubeletConfigMaxPods(t *testing.T) {
	autoNodeSizing := true

	// kc1: systemReservedCgroup: "", enforceNodeAllocatable: [pods]
	kcRaw1, err := kcfg.EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                100,
		SystemReservedCgroup:   "",
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey},
	}, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
	require.Nil(t, err, "failed to encode kubelet config")

	kc1 := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-101"},
		Spec: mcfgv1.KubeletConfigSpec{
			AutoSizingReserved: &autoNodeSizing,
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw1,
			},
		},
	}

	// kc2: systemReservedCgroup: /system.slice, enforceNodeAllocatable: [pods, system-reserved-compressible]
	kcRaw2, err := kcfg.EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                200,
		SystemReservedCgroup:   "/system.slice",
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
	}, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
	require.Nil(t, err, "failed to encode kubelet config")

	kc2 := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-200"},
		Spec: mcfgv1.KubeletConfigSpec{
			AutoSizingReserved: &autoNodeSizing,
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw2,
			},
		},
	}

	runTestWithKubeletCfg(t, "max-pods",
		[]string{`"?maxPods"?: (\S+)`, `"?systemReservedCgroup"?: "?([^",\n]*)"?`, `"?enforceNodeAllocatable"?:`},
		[]string{"maxPods", "systemReservedCgroup", "enforceNodeAllocatable"},
		[][]string{{"100", "200"}, {"", "/system.slice"}, {"pods", "system-reserved-compressible"}},
		kc1, kc2)
}

// runTestWithKubeletCfg creates a kubelet config and checks whether the expected updates were applied, then deletes the kubelet config and makes
// sure the node rolled back as expected
// testName is a string to identify the objects created (MCP, MC, kubeletConfig)
// regex key is the searching critera in the kubelet.conf. It is expected that a single field is in a capture group, and this field
// should equal expectedConfValue upon update
// kc1 and kc2 are the kubelet configs to update to and rollback from
func runTestWithKubeletCfg(t *testing.T, testName string, regexKey []string, stringKey []string, expectedConfVals [][]string, kc1, kc2 *mcfgv1.KubeletConfig) {
	cs := framework.NewClientSet("")
	matchValue := fmt.Sprintf("%s", testName)
	kcName1 := fmt.Sprintf("kubelet-%s", kc1.GetName())
	kcName2 := ""
	if kc2 != nil {
		kcName2 = fmt.Sprintf("kubelet-%s", kc2.GetName())
	}
	poolName := fmt.Sprintf("node-%s", matchValue)
	mcName := fmt.Sprintf("mc-%s", matchValue)

	// instead of a bunch of individual defers, we can run through all of them
	// in a single one
	cleanupFuncs := make([]func(), 0)
	defer func() {
		for _, f := range cleanupFuncs {
			f()
		}
	}()

	// label one node from the pool to specify which worker to update
	cleanupFuncs = append(cleanupFuncs, helpers.LabelRandomNodeFromPool(t, cs, "worker", helpers.MCPNameToRole(poolName)))
	// upon cleaning up, we need to wait for the pool to reconcile after unlabelling
	cleanupFuncs = append(cleanupFuncs, func() {
		// the sleep allows the unlabelling to take effect
		time.Sleep(time.Second * 5)
		// wait until worker pool updates the node we labelled before we delete the test specific mc and mcp
		if err := helpers.WaitForPoolComplete(t, cs, "worker", helpers.GetMcName(t, cs, "worker")); err != nil {
			t.Logf("failed to wait for pool %v", err)
		}
	})

	// cache the old configuration value to check against later
	node := helpers.GetSingleNodeByRole(t, cs, poolName)
	// the kubelet.conf format is yaml when in the default state and becomes a json when we apply a kubelet config CR
	defaultConfVals := []string{}
	for i, val := range regexKey {
		if strings.Contains(val, "systemReserved") && stringKey[i] != "systemReservedCgroup" && stringKey[i] != "enforceNodeAllocatable" {
			defaultConfVals = append(defaultConfVals, "")
			continue
		}
		out, _ := getValueFromKubeletConfig(t, cs, node, val, stringKey[i], kubeletPath)
		defaultConfVals = append(defaultConfVals, out)

		// Verify default state for systemReservedCgroup and enforceNodeAllocatable before applying kc1
		if stringKey[i] == "systemReservedCgroup" {
			require.True(t, strings.Contains(out, "/system.slice"), "default systemReservedCgroup should be '/system.slice'")
			t.Logf("Verified default systemReservedCgroup: /system.slice")
			continue // Skip early exit check for this field since we're testing disable/re-enable
		}
		if stringKey[i] == "enforceNodeAllocatable" {
			require.True(t, strings.Contains(out, "pods"), "default enforceNodeAllocatable should contain 'pods'")
			require.True(t, strings.Contains(out, "system-reserved-compressible"), "default enforceNodeAllocatable should contain 'system-reserved-compressible'")
			t.Logf("Verified default enforceNodeAllocatable contains: pods, system-reserved-compressible")
			continue // Skip early exit check for this field since we're testing disable/re-enable
		}

		for _, expect := range expectedConfVals[i] {
			if defaultConfVals[i] == expect {
				t.Logf("default configuration value %s same as values being tested against. Consider updating the test", defaultConfVals[i])
				return
			}
		}
	}

	// create an MCP to match the node we tagged
	cleanupFuncs = append(cleanupFuncs, helpers.CreateMCP(t, cs, poolName))

	// create default mc to have something to verify we successfully rolled back
	defaultMCConfig := helpers.CreateMC(mcName, poolName)
	_, err := cs.MachineConfigs().Create(context.TODO(), defaultMCConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		err := cs.MachineConfigs().Delete(context.TODO(), defaultMCConfig.Name, metav1.DeleteOptions{})
		require.Nil(t, err, "machine config deletion failed")
	})
	defaultTarget := helpers.WaitForConfigAndPoolComplete(t, cs, poolName, defaultMCConfig.Name)

	// create our first kubelet config and attach it to our created node pool
	cleanupKcFunc1 := createKcWithConfig(t, cs, kcName1, poolName, &kc1.Spec, "")
	// wait for the first kubelet config to show up
	kcMCName1, err := getMCFromKubeletCfg(t, cs, kcName1)
	require.Nil(t, err, "failed to render machine config from first container runtime config")
	// ensure the first kubelet config update rolls out to the pool
	kc1Target := helpers.WaitForConfigAndPoolComplete(t, cs, poolName, kcMCName1)
	// verify value was changed to match that of the first kubelet config
	for i, val := range regexKey {
		out, found := getValueFromKubeletConfig(t, cs, node, val, stringKey[i], kubeletPath)
		if found {
			require.Equal(t, expectedConfVals[i][0], out, "value in kubelet config not updated as expected")
		} else { // sometimes it seems regexp does not work here
			require.True(t, strings.Contains(out, expectedConfVals[i][0]))
		}
		// Additional validation for enforceNodeAllocatable to ensure "pods" is present
		if stringKey[i] == "enforceNodeAllocatable" {
			require.True(t, strings.Contains(out, "pods"), "enforceNodeAllocatable should contain 'pods'")
		}
	}

	if kc2 != nil {
		// create our second kubelet config and attach it to our created node pool
		cleanupKcFunc2 := createKcWithConfig(t, cs, kcName2, poolName, &kc2.Spec, "1")
		// wait for the second kubelet config to show up
		kcMCName2, err := getMCFromKubeletCfg(t, cs, kcName2)
		require.Nil(t, err, "failed to render machine config from second container runtime config")
		// ensure the second kubelet config update rolls out to the pool
		helpers.WaitForConfigAndPoolComplete(t, cs, poolName, kcMCName2)
		// verify value was changed to match that of the second kubelet config
		for i, val := range regexKey {
			out, found := getValueFromKubeletConfig(t, cs, node, val, stringKey[i], kubeletPath)
			if found {
				require.Equal(t, out, expectedConfVals[i][1], "value in kubelet config not updated as expected")
			} else { // sometimes it seems regexp does not work here
				require.True(t, strings.Contains(out, expectedConfVals[i][1]))
			}
			// Additional validation for enforceNodeAllocatable to ensure both "pods" and "system-reserved-compressible" are present
			if stringKey[i] == "enforceNodeAllocatable" && kc2.Spec.AutoSizingReserved != nil && *kc2.Spec.AutoSizingReserved {
				require.True(t, strings.Contains(out, "pods"), "enforceNodeAllocatable should contain 'pods'")
				require.True(t, strings.Contains(out, "system-reserved-compressible"), "enforceNodeAllocatable should contain 'system-reserved-compressible'")
			}
		}

		// cleanup the second kubelet config and make sure it doesn't error
		err = cleanupKcFunc2()
		require.Nil(t, err)
		t.Logf("Deleted KubeletConfig %s", kcName2)

		// ensure config rolls back to the previous kubelet config as expected
		helpers.WaitForPoolComplete(t, cs, poolName, kc1Target)
		// verify that the config value rolled back to that from the first kubelet config
		for i, val := range regexKey {
			out, found := getValueFromKubeletConfig(t, cs, node, val, stringKey[i], kubeletPath)
			if found {
				require.Equal(t, out, expectedConfVals[i][0], "value in kubelet config not updated as expected")
			} else { // sometimes it seems regexp does not work here
				require.True(t, strings.Contains(out, expectedConfVals[i][0]))
			}
			// Additional validation for enforceNodeAllocatable to ensure "pods" is present
			if stringKey[i] == "enforceNodeAllocatable" {
				require.True(t, strings.Contains(out, "pods"), "enforceNodeAllocatable should contain 'pods'")
			}
		}

		// cleanup the first kubelet config and make sure it doesn't error
		err = cleanupKcFunc1()
		require.Nil(t, err)
		t.Logf("Deleted KubeletConfig %s", kcName1)
		// ensure config rolls back as expected
		helpers.WaitForPoolComplete(t, cs, poolName, defaultTarget)
		// verify that the config value rolled back to the default value
		for i, val := range regexKey {
			out, found := getValueFromKubeletConfig(t, cs, node, val, stringKey[i], kubeletPath)
			// Verify default state has system-reserved-compressible enabled
			if stringKey[i] == "enforceNodeAllocatable" {
				require.True(t, strings.Contains(out, "pods"), "enforceNodeAllocatable should contain 'pods' in default state")
				require.True(t, strings.Contains(out, "system-reserved-compressible"), "enforceNodeAllocatable should contain 'system-reserved-compressible' in default state")
				continue
			}
			if stringKey[i] == "systemReservedCgroup" {
				require.True(t, strings.Contains(out, "/system.slice"), "systemReservedCgroup should be '/system.slice' in default state")
				continue
			}
			if found {
				require.Equal(t, out, defaultConfVals[i], "value in kubelet config not updated as expected")
			} else { // sometimes it seems regexp does not work here
				require.True(t, strings.Contains(out, defaultConfVals[i]))
			}
		}
	}
}

// createKcWithConfig takes a config spec and creates a KubeletConfig object
// to use this kubelet config, create a pool label key
// this function assumes there is a mcp with label 'key='
func createKcWithConfig(t *testing.T, cs *framework.ClientSet, name, key string, config *mcfgv1.KubeletConfigSpec, suffix string) func() error {
	kc := &mcfgv1.KubeletConfig{}
	kc.ObjectMeta = metav1.ObjectMeta{
		Name: name,
	}

	spec := mcfgv1.KubeletConfigSpec{
		AutoSizingReserved: config.AutoSizingReserved,
		MachineConfigPoolSelector: &metav1.LabelSelector{
			MatchLabels: make(map[string]string),
		},
		KubeletConfig: config.KubeletConfig,
	}
	spec.MachineConfigPoolSelector.MatchLabels[key] = ""
	kc.Spec = spec
	// Add the suffix value the MC created for this kubelet config should have. This helps decide
	// the priority order
	if suffix != "" {
		kc.Annotations = map[string]string{
			ctrlcommon.MCNameSuffixAnnotationKey: suffix,
		}
	}

	_, err := cs.KubeletConfigs().Create(context.TODO(), kc, metav1.CreateOptions{})
	require.Nil(t, err)
	return func() error {
		return cs.KubeletConfigs().Delete(context.TODO(), name, metav1.DeleteOptions{})
	}
}

// getMCFromKubeletCfg returns a rendered machine config that was generated from the kubelet config kcName
func getMCFromKubeletCfg(t *testing.T, cs *framework.ClientSet, kcName string) (string, error) {
	var mcName string
	// get the machine config created when we deploy the kubelet config
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcs, err := cs.MachineConfigs().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcs.Items {
			ownerRefs := mc.GetOwnerReferences()
			for _, ownerRef := range ownerRefs {
				// TODO can't find anywhere this value is defined publically
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

// getValueFromKubeletConfig jumps onto the node and gets the kubelet config. It then uses the regexKey to
// find the value that is being searched for
// regexKey is expected to be in the form `"key": (\S+)` to search for the value of key
func getValueFromKubeletConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node, regexKey, stringKey, confPath string) (string, bool) {
	// get the contents of the kubelet.conf on nodeName
	out := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", confPath))
	t.Log(out)

	// search based on the regex key. The output should have two members:
	// one with the entire line `value = key` and one with just the key, in that order
	re := regexp.MustCompile(regexKey)
	matches := re.FindStringSubmatch(string(out))
	if len(matches) != 2 && strings.Contains(string(out), stringKey) {
		return string(out), false
	}

	// If the field is not found in the YAML, it means it's empty/omitted (due to omitempty tags)
	// This is expected for fields like systemReservedCgroup when set to empty string
	if len(matches) == 0 {
		return "", true
	}

	require.Len(t, matches, 2, fmt.Sprintf("failed to get %s", regexKey))

	require.NotEmpty(t, matches[1], "regex %s attempted on kubelet config of node %s came back empty", node.Name, regexKey)
	return matches[1], true
}
