package e2e_2of2_test

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	ctrcfg "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
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
	defaultPath          = "/etc/crio/crio.conf.d/00-default"
	crioDropinDir        = "/etc/crio/crio.conf.d"
	registriesConfigPath = "/etc/containers/registries.conf"
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

func TestICSPConfigMirror(t *testing.T) {
	icspRule := &apioperatorsv1alpha1.ImageContentSourcePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-digest-only"},
		Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
			RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
				{Source: "registry.access.redhat.com/ubi8/icsp-ubi-minimal", Mirrors: []string{"mirror.com/ubi8/ubi-minimal"}},
			},
		},
	}
	runTestWithICSP(t, "digest-only", icspRule.Spec.RepositoryDigestMirrors[0].Source, icspRule)
}

// runTestWithCtrcfg creates a ctrcfg and checks whether the expected updates were applied, then deletes the ctrcfg and makes
// sure the node rolled back as expected
// testName is a string to identify the objects created (MCP, MC, ctrcfg)
// regex key is the searching critera in the crio.conf. It is expected that a single field is in a capture group, and this field
// should equal expectedConfValue upon update
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
	defaultConfVal := getValueFromCrioConfig(t, cs, node, regexKey, defaultPath)
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

	// create our first ctrcfg and attach it to our created node pool
	cleanupCtrcfgFunc1 := createCtrcfgWithConfig(t, cs, ctrcfgName1, poolName, cfg1.Spec.ContainerRuntimeConfig, "")
	// wait for the first ctrcfg to show up
	ctrcfgMCName1, err := getMCFromCtrcfg(t, cs, ctrcfgName1)
	require.Nil(t, err, "failed to render machine config from first container runtime config")
	// ensure the first ctrcfg update rolls out to the pool
	ctrcfg1Target := helpers.WaitForConfigAndPoolComplete(t, cs, poolName, ctrcfgMCName1)
	// verify value was changed to match that of the first ctrcfg
	firstConfValue := getValueFromCrioConfig(t, cs, node, regexKey, ctrcfg.CRIODropInFilePathLogLevel)
	require.Equal(t, firstConfValue, expectedConfVal1, "value in crio config not updated as expected")

	// create our second ctrcfg and attach it to our created node pool
	cleanupCtrcfgFunc2 := createCtrcfgWithConfig(t, cs, ctrcfgName2, poolName, cfg2.Spec.ContainerRuntimeConfig, "1")
	// wait for the second ctrcfg to show up
	ctrcfgMCName2, err := getMCFromCtrcfg(t, cs, ctrcfgName2)
	require.Nil(t, err, "failed to render machine config from second container runtime config")
	// ensure the second ctrcfg update rolls out to the pool
	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, ctrcfgMCName2)
	// verify value was changed to match that of the first ctrcfg
	secondConfValue := getValueFromCrioConfig(t, cs, node, regexKey, ctrcfg.CRIODropInFilePathLogLevel)
	require.Equal(t, secondConfValue, expectedConfVal2, "value in crio config not updated as expected")

	// cleanup the second ctrcfg and make sure it doesn't error
	err = cleanupCtrcfgFunc2()
	require.Nil(t, err)
	t.Logf("Deleted ContainerRuntimeConfig %s", ctrcfgName2)
	// ensure config rolls back to the previous ctrcfg as expected
	helpers.WaitForPoolComplete(t, cs, poolName, ctrcfg1Target)
	// verify that the config value rolled back to that from the first ctrcfg
	rollbackConfValue := getValueFromCrioConfig(t, cs, node, regexKey, ctrcfg.CRIODropInFilePathLogLevel)
	require.Equal(t, rollbackConfValue, expectedConfVal1, "ctrcfg deletion didn't cause node to roll back to previous ctrcfg config")

	// cleanup the first ctrcfg and make sure it doesn't error
	err = cleanupCtrcfgFunc1()
	require.Nil(t, err)
	t.Logf("Deleted ContainerRuntimeConfig %s", ctrcfgName1)
	// ensure config rolls back as expected
	helpers.WaitForPoolComplete(t, cs, poolName, defaultTarget)

	restoredConfValue := getValueFromCrioConfig(t, cs, node, regexKey, defaultPath)
	require.Equal(t, restoredConfValue, defaultConfVal, "ctrcfg deletion didn't cause node to roll back to default config")
	// Verify that the override file doesn't exist in crio.conf.d anymore
	arr := strings.Split(ctrcfg.CRIODropInFilePathLogLevel, "/")
	overrideFileName := arr[len(arr)-1]
	exists, err := fileExists(t, cs, node, overrideFileName)
	require.NoError(t, err)
	if exists {
		err := fmt.Errorf("override file still exists in crio.conf.d")
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
		return "", fmt.Errorf("can't find machine config created by ctrcfg %s: %w", ctrcfgName, err)
	}
	return mcName, nil
}

// getValueFromCrioConfig jumps onto the node and gets the crio config. It then uses the regexKey to
// find the value that is being searched for
// regexKey is expected to be in the form `key = (\S+)` to search for the value of key
func getValueFromCrioConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node, regexKey, confPath string) string {
	// get the contents of the crio.conf on nodeName
	out := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", confPath))

	// search based on the regex key. The output should have two members:
	// one with the entire line `value = key` and one with just the key, in that order
	re := regexp.MustCompile(regexKey)
	matches := re.FindStringSubmatch(string(out))
	require.Len(t, matches, 2)

	require.NotEmpty(t, matches[1], "regex %s attempted on crio config of node %s came back empty", node.Name, regexKey)
	return matches[1]
}

// fileExists checks whether overrideFile exists in /etc/crio/crio.conf.d.
// Returns (false, err) on exec errors (e.g. connection refused during node reboot)
// so callers using wait.Poll can retry.
func fileExists(t *testing.T, cs *framework.ClientSet, node corev1.Node, overrideFile string) (bool, error) {
	out, err := helpers.ExecCmdOnNodeWithError(cs, node, "ls", filepath.Join("/rootfs", crioDropinDir))
	if err != nil {
		t.Logf("fileExists: exec failed on node %s (may be transient during node reboot): %v, output: %q", node.Name, err, out)
		return false, err
	}
	return strings.Contains(string(out), overrideFile), nil
}

func runTestWithICSP(t *testing.T, testName, expectedVal string, icspRule *apioperatorsv1alpha1.ImageContentSourcePolicy) {
	cs := framework.NewClientSet("")
	icspObjName := fmt.Sprintf("icsp-%s", icspRule.GetName())
	labelName := fmt.Sprintf("node-%s", testName)
	poolName := fmt.Sprintf("node-%s", testName)
	searchKey := icspRule.Spec.RepositoryDigestMirrors[0].Source
	// instead of a bunch of individual defers, we can run through all of them
	// in a single one
	cleanupFuncs := make([]func(), 0)
	defer func() {
		for _, f := range cleanupFuncs {
			f()
		}
	}()

	// label one node from the pool to specify which worker to update
	cleanupFuncs = append(cleanupFuncs, helpers.LabelRandomNodeFromPool(t, cs, "worker", helpers.MCPNameToRole(labelName)))
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
	node := helpers.GetSingleNodeByRole(t, cs, labelName)
	// cache the old configuration value to check against later
	defaultConfVal := getValueFromRegistriesConfig(t, cs, node, searchKey, registriesConfigPath)
	if defaultConfVal == expectedVal {
		t.Logf("default configuration value %s same as values being tested against. Consider updating the test", defaultConfVal)
		return
	}

	// create an MCP to match the node we tagged
	cleanupFuncs = append(cleanupFuncs, helpers.CreateMCP(t, cs, poolName))

	// create icsp and attach it to our cluster
	cleanupICSPFunc := createICSPWithConfig(t, cs, icspObjName, icspRule.Spec.RepositoryDigestMirrors)
	// wait for the icsp to show up
	// ensure the first icsp update rolls out to the cluster
	err := helpers.WaitForPoolCompleteAny(t, cs, poolName)
	require.Nil(t, err)
	// verify value was changed to match that of the icsp
	confVal := getValueFromRegistriesConfig(t, cs, node, searchKey, registriesConfigPath)
	require.Equal(t, expectedVal, confVal, "value in registries config not updated as expected")

	// cleanup the icsp and make sure it doesn't error
	err = cleanupICSPFunc()
	require.Nil(t, err)
	t.Logf("Deleted icsp %s", icspObjName)
	// ensure config rolls back as expected
	err = helpers.WaitForPoolCompleteAny(t, cs, poolName)
	require.Nil(t, err)
}

func createICSPWithConfig(t *testing.T, cs *framework.ClientSet, name string, config []apioperatorsv1alpha1.RepositoryDigestMirrors) func() error {
	icspRule := &apioperatorsv1alpha1.ImageContentSourcePolicy{}
	icspRule.ObjectMeta = metav1.ObjectMeta{
		Name: name,
	}
	icspRule.Spec.RepositoryDigestMirrors = config
	_, err := cs.ImageContentSourcePolicies().Create(context.TODO(), icspRule, metav1.CreateOptions{})
	require.Nil(t, err)
	return func() error {
		return cs.ImageContentSourcePolicies().Delete(context.TODO(), name, metav1.DeleteOptions{})
	}
}

func getValueFromRegistriesConfig(t *testing.T, cs *framework.ClientSet, node corev1.Node, searchKey, confPath string) string {
	// get the contents of the registries.conf on nodeName
	out := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", confPath))

	// search based on the regex key. The output should have two members:
	// one with the entire line `value = key` and one with just the key, in that order
	if strings.Contains(out, searchKey) {
		return searchKey
	}
	return ""
}

// TestKubeletConfigTLSPropagationToCRIO verifies that setting a TLSSecurityProfile
// in a KubeletConfig CR causes the ContainerRuntimeConfig controller to generate a
// CRI-O drop-in config file with the corresponding tls_min_version. This ensures
// both kubelet and CRI-O use the same TLS settings when configured via KubeletConfig.
func TestKubeletConfigTLSPropagationToCRIO(t *testing.T) {
	cs := framework.NewClientSet("")
	testName := "tls-propagation"
	poolName := fmt.Sprintf("node-%s", testName)
	mcName := fmt.Sprintf("mc-%s", testName)
	ctrcfgName := fmt.Sprintf("ctrcfg-%s", testName)
	kcName := fmt.Sprintf("kubelet-tls-%s", testName)
	tlsDropinPath := ctrcfg.CRIODropInFilePathTLSMinVersion

	cleanupFuncs := make([]func(), 0)
	defer func() {
		for _, f := range cleanupFuncs {
			f()
		}
	}()

	// Label a worker node for an isolated MCP
	cleanupFuncs = append(cleanupFuncs, helpers.LabelRandomNodeFromPool(t, cs, "worker", helpers.MCPNameToRole(poolName)))
	cleanupFuncs = append(cleanupFuncs, func() {
		time.Sleep(time.Second * 5)
		if err := helpers.WaitForPoolComplete(t, cs, "worker", helpers.GetMcName(t, cs, "worker")); err != nil {
			t.Logf("failed to wait for pool %v", err)
		}
	})

	node := helpers.GetSingleNodeByRole(t, cs, poolName)

	// Create an isolated MCP
	cleanupFuncs = append(cleanupFuncs, helpers.CreateMCP(t, cs, poolName))

	// Create a default MC for rollback verification
	defaultMCConfig := helpers.CreateMC(mcName, poolName)
	_, err := cs.MachineConfigs().Create(context.TODO(), defaultMCConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		err := cs.MachineConfigs().Delete(context.TODO(), defaultMCConfig.Name, metav1.DeleteOptions{})
		require.Nil(t, err, "machine config deletion failed")
	})
	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, defaultMCConfig.Name)

	// Create a ContainerRuntimeConfig targeting our pool.
	ctrcfgObj := &mcfgv1.ContainerRuntimeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: ctrcfgName},
		Spec: mcfgv1.ContainerRuntimeConfigSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{poolName: ""},
			},
			ContainerRuntimeConfig: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "debug",
			},
		},
	}
	_, err = cs.ContainerRuntimeConfigs().Create(context.TODO(), ctrcfgObj, metav1.CreateOptions{})
	require.Nil(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		_ = cs.ContainerRuntimeConfigs().Delete(context.TODO(), ctrcfgName, metav1.DeleteOptions{})
	})

	// Wait for the ContainerRuntimeConfig MC to appear and pool to converge
	ctrcfgMCName, err := getMCFromCtrcfg(t, cs, ctrcfgName)
	require.Nil(t, err, "failed to render machine config from container runtime config")
	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, ctrcfgMCName)

	// Verify that no TLS drop-in exists yet (no KubeletConfig with TLS)
	tlsDropinFileName := filepath.Base(tlsDropinPath)
	exists, err := fileExists(t, cs, node, tlsDropinFileName)
	require.NoError(t, err, "failed to check for TLS drop-in before KubeletConfig creation")
	require.False(t, exists,
		"TLS drop-in should not exist before KubeletConfig with TLS is created")

	// Create a KubeletConfig with TLS 1.3 targeting the same pool.
	kcRaw, err := kcfg.EncodeKubeletConfig(
		&kubeletconfigv1beta1.KubeletConfiguration{},
		kubeletconfigv1beta1.SchemeGroupVersion,
		runtime.ContentTypeJSON,
	)
	require.Nil(t, err, "failed to encode kubelet config")

	kcObj := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: kcName},
		Spec: mcfgv1.KubeletConfigSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{poolName: ""},
			},
			KubeletConfig: &runtime.RawExtension{Raw: kcRaw},
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS13,
					},
				},
			},
		},
	}
	_, err = cs.KubeletConfigs().Create(context.TODO(), kcObj, metav1.CreateOptions{})
	require.Nil(t, err)
	cleanupFuncs = append(cleanupFuncs, func() {
		_ = cs.KubeletConfigs().Delete(context.TODO(), kcName, metav1.DeleteOptions{})
	})

	// Wait for the pool to converge after the KubeletConfig is applied.
	err = wait.PollImmediate(5*time.Second, 15*time.Minute, func() (bool, error) {
		// Check if the TLS drop-in has appeared on the node (retry on transient exec errors)
		exists, err := fileExists(t, cs, node, tlsDropinFileName)
		if err != nil {
			return false, nil
		}
		return exists, nil
	})
	require.Nil(t, err, "TLS drop-in file did not appear on node after KubeletConfig creation")

	// Verify the drop-in content has the correct TLS version
	tlsConfValue := getValueFromCrioConfig(t, cs, node, `tls_min_version = "(\S+)"`, tlsDropinPath)
	require.Equal(t, "VersionTLS13", tlsConfValue,
		"CRI-O TLS drop-in should contain VersionTLS13")
	t.Logf("Verified CRI-O TLS drop-in contains tls_min_version = VersionTLS13")

	// Update the KubeletConfig from TLS 1.3 to TLS 1.2.
	kcUpdated, err := cs.KubeletConfigs().Get(context.TODO(), kcName, metav1.GetOptions{})
	require.Nil(t, err, "failed to get KubeletConfig for update")
	kcUpdated.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
			},
		},
	}
	_, err = cs.KubeletConfigs().Update(context.TODO(), kcUpdated, metav1.UpdateOptions{})
	require.Nil(t, err, "failed to update KubeletConfig to TLS 1.2")
	t.Logf("Updated KubeletConfig %s from TLS 1.3 to TLS 1.2", kcName)

	// Wait for the drop-in to reflect the updated TLS version
	err = wait.PollImmediate(5*time.Second, 15*time.Minute, func() (bool, error) {
		val := getValueFromCrioConfig(t, cs, node, `tls_min_version = "(\S+)"`, tlsDropinPath)
		return val == "VersionTLS12", nil
	})
	require.Nil(t, err, "TLS drop-in was not updated to VersionTLS12 after KubeletConfig update")
	t.Logf("Verified CRI-O TLS drop-in updated to tls_min_version = VersionTLS12")

	// Step 4: Delete the KubeletConfig and verify the TLS drop-in is removed.
	err = cs.KubeletConfigs().Delete(context.TODO(), kcName, metav1.DeleteOptions{})
	require.Nil(t, err, "failed to delete KubeletConfig")
	t.Logf("Deleted KubeletConfig %s", kcName)

	// Wait for the pool to finish the rollback update. MCO rollback can take longer
	// (node reboot, pool convergence), so use a longer interval and timeout.
	err = wait.PollImmediate(15*time.Second, 25*time.Minute, func() (bool, error) {
		exists, err := fileExists(t, cs, node, tlsDropinFileName)
		if err != nil {
			return false, nil
		}
		return !exists, nil
	})
	require.Nil(t, err, "TLS drop-in should be removed after KubeletConfig deletion")
	t.Logf("Verified CRI-O TLS drop-in was removed after KubeletConfig deletion")
}
