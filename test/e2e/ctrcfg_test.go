package e2e_test

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestContainerRuntimeConfigPidsLimit(t *testing.T) {
	runTestWithCtrcfg(t, "pids-limit", `pids_limit = (\S+)`, "12345", &mcfgv1.ContainerRuntimeConfiguration{
		PidsLimit: 12345,
	})
}

// runTestWithCtrcfg creates a ctrcfg and checks whether the expected updates were applied, then deletes the ctrcfg and makes
// sure the node rolled back as expected
// testName is a string to identify the objects created (MCP, MC, ctrcfg)
// regex key is the searching critera in the crio.conf. It is expected that a single field is in a capture group, and this field
//   should equal expectedConfValue upon update
// cfg is the ctrcfg config to update to and rollback from
func runTestWithCtrcfg(t *testing.T, testName, regexKey, expectedConfValue string, cfg *mcfgv1.ContainerRuntimeConfiguration) {
	cs := framework.NewClientSet("")
	matchValue := fmt.Sprintf("%s-%s", testName, uuid.NewUUID())
	ctrcfgName := fmt.Sprintf("ctrcfg-%s", matchValue)
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
	oldConfValue := getValueFromCrioConfig(t, cs, node, regexKey)
	if oldConfValue == expectedConfValue {
		t.Logf("default configuration value %s same as value being tested against. Consider updating the test", oldConfValue)
		return
	}

	// create an MCP to match the node we tagged
	cleanupFuncs = append(cleanupFuncs, createMCP(t, cs, poolName))

	// create old mc to have something to verify we successfully rolled back
	oldMCConfig := createMCV3(mcName, poolName)
	_, err := cs.MachineConfigs().Create(oldMCConfig)
	require.Nil(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		err := cs.MachineConfigs().Delete(oldMCConfig.Name, &metav1.DeleteOptions{})
		require.Nil(t, err, "machine config deletion failed")
	})
	waitForConfigAndPoolComplete(t, cs, poolName, oldMCConfig.Name)

	// create our ctrcfg and attach it to our created node pool
	cleanupCtrcfgFunc := createCtrcfgWithConfig(t, cs, ctrcfgName, poolName, cfg)

	// wait for the ctrcfg to show up
	ctrcfgMCName, err := getMCFromCtrcfg(t, cs, ctrcfgName)
	require.Nil(t, err, "failed to render machine config from container runtime config")

	// ensure ctrcfg update rolls out to the pool
	waitForConfigAndPoolComplete(t, cs, poolName, ctrcfgMCName)

	// verify value was changed
	newConfValue := getValueFromCrioConfig(t, cs, node, regexKey)
	require.Equal(t, newConfValue, expectedConfValue, "value in crio.conf not updated as expected")

	// cleanup ctrcfg and make sure it doesn't error
	err = cleanupCtrcfgFunc()
	require.Nil(t, err)

	t.Logf("Deleted ContainerRuntimeConfig %s", ctrcfgName)
	// there's a weird race where we observe the pool is updated when in reality
	// that update is from before. Sleeping allows a new update cycle to start
	time.Sleep(time.Second * 5)

	// ensure config rolls back as expected
	waitForConfigAndPoolComplete(t, cs, poolName, oldMCConfig.Name)

	restoredConfValue := getValueFromCrioConfig(t, cs, node, regexKey)
	require.Equal(t, restoredConfValue, oldConfValue, "ctrcfg deletion didn't cause node to roll back config")
}

// createCtrcfgWithConfig takes a config spec and creates a ContainerRuntimeConfig object
// to use this ctrcfg, create a pool label key
// this function assumes there is a mcp with label 'key='
func createCtrcfgWithConfig(t *testing.T, cs *framework.ClientSet, name, key string, config *mcfgv1.ContainerRuntimeConfiguration) func() error {
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

	_, err := cs.ContainerRuntimeConfigs().Create(ctrcfg)
	require.Nil(t, err)
	return func() error {
		return cs.ContainerRuntimeConfigs().Delete(name, &metav1.DeleteOptions{})
	}
}

// getMCFromCtrcfg returns a rendered machine config that was generated from the ctrcfg ctrcfgName
func getMCFromCtrcfg(t *testing.T, cs *framework.ClientSet, ctrcfgName string) (string, error) {
	var mcName string

	// get the machine config created when we deploy the ctrcfg
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcs, err := cs.MachineConfigs().List(metav1.ListOptions{})
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
func getValueFromCrioConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node, regexKey string) string {
	// get the contents of the crio.conf on nodeName
	out := execCmdOnNode(t, cs, node, "cat", "/rootfs/etc/crio/crio.conf")

	// search based on the regex key. The output should have two members:
	// one with the entire line `value = key` and one with just the key, in that order
	re := regexp.MustCompile(regexKey)
	matches := re.FindStringSubmatch(string(out))
	require.Len(t, matches, 2)

	require.NotEmpty(t, matches[1], "regex %s attempted on crio.conf of node %s came back empty", node.Name, regexKey)
	return matches[1]
}
