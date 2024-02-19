package e2e_test

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
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
)

const (
	kubeletPath = "/etc/kubernetes/kubelet.conf"
)

func TestKubeletConfigMaxPods(t *testing.T) {
	kcRaw1, err := kcfg.EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, kubeletconfigv1beta1.SchemeGroupVersion)
	require.Nil(t, err, "failed to encode kubelet config")
	autoNodeSizing := true
	kc1 := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-100"},
		Spec: mcfgv1.KubeletConfigSpec{
			AutoSizingReserved: &autoNodeSizing,
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw1,
			},
		},
	}
	kcRaw2, err := kcfg.EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 200}, kubeletconfigv1beta1.SchemeGroupVersion)
	require.Nil(t, err, "failed to encode kubelet config")
	kc2 := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-200"},
		Spec: mcfgv1.KubeletConfigSpec{
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw2,
			},
		},
	}

	runTestWithKubeletCfg(t, "max-pods", `"?maxPods"?: (\S+)`, "100,", "200,", kc1, kc2)
}

// runTestWithKubeletCfg creates a kubelet config and checks whether the expected updates were applied, then deletes the kubelet config and makes
// sure the node rolled back as expected
// testName is a string to identify the objects created (MCP, MC, kubeletConfig)
// regex key is the searching critera in the kubelet.conf. It is expected that a single field is in a capture group, and this field
// should equal expectedConfValue upon update
// kc1 and kc2 are the kubelet configs to update to and rollback from
func runTestWithKubeletCfg(t *testing.T, testName, regexKey, expectedConfVal1, expectedConfVal2 string, kc1, kc2 *mcfgv1.KubeletConfig) {
	cs := framework.NewClientSet("")
	matchValue := fmt.Sprintf("%s", testName)
	kcName1 := fmt.Sprintf("kubelet-%s", kc1.GetName())
	kcName2 := fmt.Sprintf("kubelet-%s", kc2.GetName())
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
	defaultConfVal := getValueFromKubeletConfig(t, cs, node, `"?maxPods"?: (\S+)`, kubeletPath) + ","
	if defaultConfVal == expectedConfVal1 || defaultConfVal == expectedConfVal2 {
		t.Logf("default configuration value %s same as values being tested against. Consider updating the test", defaultConfVal)
		return
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
	firstConfValue := getValueFromKubeletConfig(t, cs, node, regexKey, kubeletPath)
	require.Equal(t, firstConfValue, expectedConfVal1, "value in kubelet config not updated as expected")
	// Get the new node object which should reflect new values for the allocatables
	refreshedNode := helpers.GetSingleNodeByRole(t, cs, poolName)

	// The value for the allocatable should have changed because of the auto node sizing.
	// We cannot predict if the values of the allocatables will increase or decrease,
	// as it depends on the configuration of the system under test.
	require.NotEqual(t, refreshedNode.Status.Allocatable.Memory().Value(), node.Status.Allocatable.Memory().Value(), "value of the allocatable should have changed")

	// create our second kubelet config and attach it to our created node pool
	cleanupKcFunc2 := createKcWithConfig(t, cs, kcName2, poolName, &kc2.Spec, "1")
	// wait for the second kubelet config to show up
	kcMCName2, err := getMCFromKubeletCfg(t, cs, kcName2)
	require.Nil(t, err, "failed to render machine config from second container runtime config")
	// ensure the second kubelet config update rolls out to the pool
	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, kcMCName2)
	// verify value was changed to match that of the first kubelet config
	secondConfValue := getValueFromKubeletConfig(t, cs, node, regexKey, kubeletPath)
	require.Equal(t, secondConfValue, expectedConfVal2, "value in kubelet config not updated as expected")

	// cleanup the second kubelet config and make sure it doesn't error
	err = cleanupKcFunc2()
	require.Nil(t, err)
	t.Logf("Deleted KubeletConfig %s", kcName2)
	// ensure config rolls back to the previous kubelet config as expected
	helpers.WaitForPoolComplete(t, cs, poolName, kc1Target)
	// verify that the config value rolled back to that from the first kubelet config
	rollbackConfValue := getValueFromKubeletConfig(t, cs, node, regexKey, kubeletPath)
	require.Equal(t, rollbackConfValue, expectedConfVal1, "kubelet config deletion didn't cause node to roll back to previous kubelet config")

	// cleanup the first kubelet config and make sure it doesn't error
	err = cleanupKcFunc1()
	require.Nil(t, err)
	t.Logf("Deleted KubeletConfig %s", kcName1)
	// ensure config rolls back as expected
	helpers.WaitForPoolComplete(t, cs, poolName, defaultTarget)
	// verify that the config value rolled back to the default value
	restoredConfValue := getValueFromKubeletConfig(t, cs, node, `"?maxPods"?: (\S+)`, kubeletPath) + ","
	require.Equal(t, restoredConfValue, defaultConfVal, "kubelet config deletion didn't cause node to roll back to default config")
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
func getValueFromKubeletConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node, regexKey, confPath string) string {
	// get the contents of the kubelet.conf on nodeName
	out := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", confPath))

	// search based on the regex key. The output should have two members:
	// one with the entire line `value = key` and one with just the key, in that order
	re := regexp.MustCompile(regexKey)
	matches := re.FindStringSubmatch(string(out))
	require.Len(t, matches, 2)

	require.NotEmpty(t, matches[1], "regex %s attempted on kubelet config of node %s came back empty", node.Name, regexKey)
	return matches[1]
}
