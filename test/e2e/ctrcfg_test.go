package e2e_test

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	ctrcfg "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	defaultPath   = "/etc/crio/crio.conf.d/00-default"
	crioDropinDir = "/etc/crio/crio.conf.d"
)

func TestContainerRuntimeConfigLogLevel(t *testing.T) {
	ctrcfg1 := &mcfgv1.ContainerRuntimeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-debug"},
		Spec: mcfgv1.ContainerRuntimeConfigSpec{
			ContainerRuntimeConfig: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "debug",
			},
		},
	}
	ctrcfg2 := &mcfgv1.ContainerRuntimeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-warn"},
		Spec: mcfgv1.ContainerRuntimeConfigSpec{
			ContainerRuntimeConfig: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "warn",
			},
		},
	}
	runTestWithCtrcfg(t, "log-level", `log_level = (\S+)`, "\"debug\"", "\"warn\"", ctrcfg1, ctrcfg2)
}

// runTestWithCtrcfg creates a ctrcfg and checks whether the expected updates were applied, then deletes the ctrcfg and makes
// sure the node rolled back as expected
// testName is a string to identify the objects created (MCP, MC, ctrcfg)
// regex key is the searching critera in the crio.conf. It is expected that a single field is in a capture group, and this field
//   should equal expectedConfValue upon update
// cfg is the ctrcfg config to update to and rollback from
func runTestWithCtrcfg(t *testing.T, testName, regexKey, expectedConfVal1, expectedConfVal2 string, cfg1, cfg2 *mcfgv1.ContainerRuntimeConfig) {
	cs := framework.NewClientSet("")
	matchValue := fmt.Sprintf("%s", testName)
	ctrcfgName1 := fmt.Sprintf("ctrcfg-%s", cfg1.GetName())
	ctrcfgName2 := fmt.Sprintf("ctrcfg-%s", cfg2.GetName())
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
	cleanupFuncs = append(cleanupFuncs, labelRandomNodeFromPool(t, cs, "worker", mcpNameToRole(poolName)))
	// upon cleaning up, we need to wait for the pool to reconcile after unlabelling
	cleanupFuncs = append(cleanupFuncs, func() {
		// the sleep allows the unlabelling to take effect
		time.Sleep(time.Second * 5)
		// wait until worker pool updates the node we labelled before we delete the test specific mc and mcp
		if err := waitForPoolComplete(t, cs, "worker", getMcName(t, cs, "worker")); err != nil {
			t.Logf("failed to wait for pool %v", err)
		}
	})

	// cache the old configuration value to check against later
	node := getSingleNodeByRole(t, cs, poolName)
	defaultConfVal := getValueFromCrioConfig(t, cs, node, regexKey, defaultPath)
	if defaultConfVal == expectedConfVal1 || defaultConfVal == expectedConfVal2 {
		t.Logf("default configuration value %s same as values being tested against. Consider updating the test", defaultConfVal)
		return
	}

	// create an MCP to match the node we tagged
	cleanupFuncs = append(cleanupFuncs, createMCP(t, cs, poolName))

	// create default mc to have something to verify we successfully rolled back
	defaultMCConfig := createMC(mcName, poolName)
	_, err := cs.MachineConfigs().Create(context.TODO(), defaultMCConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		err := cs.MachineConfigs().Delete(context.TODO(), defaultMCConfig.Name, metav1.DeleteOptions{})
		require.Nil(t, err, "machine config deletion failed")
	})
	defaultTarget := waitForConfigAndPoolComplete(t, cs, poolName, defaultMCConfig.Name)

	// create our first ctrcfg and attach it to our created node pool
	cleanupCtrcfgFunc1 := createCtrcfgWithConfig(t, cs, ctrcfgName1, poolName, cfg1.Spec.ContainerRuntimeConfig, "")
	// wait for the first ctrcfg to show up
	ctrcfgMCName1, err := getMCFromCtrcfg(t, cs, ctrcfgName1)
	require.Nil(t, err, "failed to render machine config from first container runtime config")
	// ensure the first ctrcfg update rolls out to the pool
	ctrcfg1Target := waitForConfigAndPoolComplete(t, cs, poolName, ctrcfgMCName1)
	// verify value was changed to match that of the first ctrcfg
	firstConfValue := getValueFromCrioConfig(t, cs, node, regexKey, ctrcfg.CRIODropInFilePathLogLevel)
	require.Equal(t, firstConfValue, expectedConfVal1, "value in crio config not updated as expected")

	// create our second ctrcfg and attach it to our created node pool
	cleanupCtrcfgFunc2 := createCtrcfgWithConfig(t, cs, ctrcfgName2, poolName, cfg2.Spec.ContainerRuntimeConfig, "1")
	// wait for the second ctrcfg to show up
	ctrcfgMCName2, err := getMCFromCtrcfg(t, cs, ctrcfgName2)
	require.Nil(t, err, "failed to render machine config from second container runtime config")
	// ensure the second ctrcfg update rolls out to the pool
	waitForConfigAndPoolComplete(t, cs, poolName, ctrcfgMCName2)
	// verify value was changed to match that of the first ctrcfg
	secondConfValue := getValueFromCrioConfig(t, cs, node, regexKey, ctrcfg.CRIODropInFilePathLogLevel)
	require.Equal(t, secondConfValue, expectedConfVal2, "value in crio config not updated as expected")

	// cleanup the second ctrcfg and make sure it doesn't error
	err = cleanupCtrcfgFunc2()
	require.Nil(t, err)
	t.Logf("Deleted ContainerRuntimeConfig %s", ctrcfgName2)
	// ensure config rolls back to the previous ctrcfg as expected
	waitForPoolComplete(t, cs, poolName, ctrcfg1Target)
	// verify that the config value rolled back to that from the first ctrcfg
	rollbackConfValue := getValueFromCrioConfig(t, cs, node, regexKey, ctrcfg.CRIODropInFilePathLogLevel)
	require.Equal(t, rollbackConfValue, expectedConfVal1, "ctrcfg deletion didn't cause node to roll back to previous ctrcfg config")

	// cleanup the first ctrcfg and make sure it doesn't error
	err = cleanupCtrcfgFunc1()
	require.Nil(t, err)
	t.Logf("Deleted ContainerRuntimeConfig %s", ctrcfgName1)
	// ensure config rolls back as expected
	waitForPoolComplete(t, cs, poolName, defaultTarget)

	restoredConfValue := getValueFromCrioConfig(t, cs, node, regexKey, defaultPath)
	require.Equal(t, restoredConfValue, defaultConfVal, "ctrcfg deletion didn't cause node to roll back to default config")
	// Verify that the override file doesn't exist in crio.conf.d anymore
	arr := strings.Split(ctrcfg.CRIODropInFilePathLogLevel, "/")
	overrideFileName := arr[len(arr)-1]
	if fileExists(t, cs, node, overrideFileName) {
		err := errors.New("override file still exists in crio.conf.d")
		require.Nil(t, err)
	}
}

// createCtrcfgWithConfig takes a config spec and creates a ContainerRuntimeConfig object
// to use this ctrcfg, create a pool label key
// this function assumes there is a mcp with label 'key='
func createCtrcfgWithConfig(t *testing.T, cs *framework.ClientSet, name, key string, config *mcfgv1.ContainerRuntimeConfiguration, suffix string) func() error {
	ctrcfg := &mcfgv1.ContainerRuntimeConfig{}
	ctrcfg.ObjectMeta = metav1.ObjectMeta{
		Name: name,
	}
	spec := mcfgv1.ContainerRuntimeConfigSpec{
		MachineConfigPoolSelector: &metav1.LabelSelector{
			MatchLabels: make(map[string]string),
		},
		ContainerRuntimeConfig: config,
	}
	spec.MachineConfigPoolSelector.MatchLabels[key] = ""
	ctrcfg.Spec = spec
	// Add the suffix value the MC created for this ctrcfg should have. This helps decide
	// the priority order
	if suffix != "" {
		ctrcfg.Annotations = map[string]string{
			ctrlcommon.MCNameSuffixAnnotationKey: suffix,
		}
	}

	_, err := cs.ContainerRuntimeConfigs().Create(context.TODO(), ctrcfg, metav1.CreateOptions{})
	require.Nil(t, err)
	return func() error {
		return cs.ContainerRuntimeConfigs().Delete(context.TODO(), name, metav1.DeleteOptions{})
	}
}

// getMCFromCtrcfg returns a rendered machine config that was generated from the ctrcfg ctrcfgName
func getMCFromCtrcfg(t *testing.T, cs *framework.ClientSet, ctrcfgName string) (string, error) {
	var mcName string
	// get the machine config created when we deploy the ctrcfg
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcs, err := cs.MachineConfigs().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcs.Items {
			ownerRefs := mc.GetOwnerReferences()
			for _, ownerRef := range ownerRefs {
				// TODO can't find anywhere this value is defined publically
				if ownerRef.Kind == "ContainerRuntimeConfig" && ownerRef.Name == ctrcfgName {
					mcName = mc.Name
					return true, nil
				}
			}
		}
		return false, nil
	}); err != nil {
		return "", errors.Wrapf(err, "can't find machine config created by ctrcfg %s", ctrcfgName)
	}
	return mcName, nil
}

// getValueFromCrioConfig jumps onto the node and gets the crio config. It then uses the regexKey to
// find the value that is being searched for
// regexKey is expected to be in the form `key = (\S+)` to search for the value of key
func getValueFromCrioConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node, regexKey, confPath string) string {
	// get the contents of the crio.conf on nodeName
	out := execCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", confPath))

	// search based on the regex key. The output should have two members:
	// one with the entire line `value = key` and one with just the key, in that order
	re := regexp.MustCompile(regexKey)
	matches := re.FindStringSubmatch(string(out))
	require.Len(t, matches, 2)

	require.NotEmpty(t, matches[1], "regex %s attempted on crio config of node %s came back empty", node.Name, regexKey)
	return matches[1]
}

// fileExists checks whether overrideFile exists in /etc/crio/crio.conf.d
func fileExists(t *testing.T, cs *framework.ClientSet, node corev1.Node, overrideFile string) bool {
	// get the contents of the crio drop in directory
	out := execCmdOnNode(t, cs, node, "ls", filepath.Join("/rootfs", crioDropinDir))

	// Check if the overrideFile name exists in the output
	return strings.Contains(string(out), overrideFile)
}
