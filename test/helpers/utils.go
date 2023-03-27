package helpers

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
)

type CleanupFuncs struct {
	funcs []func()
}

func (c *CleanupFuncs) Add(f func()) {
	c.funcs = append(c.funcs, f)
}

func (c *CleanupFuncs) Run() {
	for _, f := range c.funcs {
		f()
	}
}

func NewCleanupFuncs() CleanupFuncs {
	return CleanupFuncs{
		funcs: []func(){},
	}
}

func ApplyMC(t *testing.T, cs *framework.ClientSet, mc *mcfgv1.MachineConfig) func() {
	_, err := cs.MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	require.Nil(t, err)

	return ToCleanupFunc(t, func() {
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}))
	})
}

// Ensures that a given cleanup function only runs once; even if called
// multiple times.
func MakeIdempotent(f func()) func() {
	hasRun := false

	return func() {
		if !hasRun {
			f()
			hasRun = true
		}
	}
}

// Ensures that a given cleanup function is registered with the test suite and
// is run only once; even if called multiple times.
func ToCleanupFunc(t *testing.T, f func()) func() {
	f = MakeIdempotent(f)
	t.Cleanup(f)
	return f
}

func IsSNO(node corev1.Node) bool {
	controlPlaneTopology := node.Annotations[constants.ClusterControlPlaneTopologyAnnotationKey]
	switch configv1.TopologyMode(controlPlaneTopology) {
	case configv1.SingleReplicaTopologyMode:
		return true
	case configv1.HighlyAvailableTopologyMode:
		return false
	default:
		// for any unhandled case, default to HighlyAvailableTopologyMode
		return false
	}
}

// Creates a pool, adds the provided node to it, applies the MachineConfig,
// waits for the rollout. Returns an idempotent function which can be used both
// for reverting and cleanup purposes.
func CreatePoolAndApplyMCToNode(t *testing.T, cs *framework.ClientSet, poolName string, node corev1.Node, mcs []*mcfgv1.MachineConfig) func() {
	workerMCPName := "worker"

	t.Logf("Setting up pool %s", poolName)

	unlabelFunc := LabelNode(t, cs, node, MCPNameToRole(poolName))
	deleteMCPFunc := CreateMCP(t, cs, poolName)

	updatedNode, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	node = *updatedNode

	t.Logf("Target node %s now has role(s): %v", node.Name, getNodeRoles(node))

	renderedMCName, deleteMCs := applyMCsAndWaitForRenderedConfigs(t, cs, poolName, mcs)

	require.NoError(t, WaitForPoolComplete(t, cs, poolName, renderedMCName), "pool %s did not update to config %s", poolName, renderedMCName)

	mcpMCName := GetMcName(t, cs, poolName)
	require.Nil(t, WaitForNodeConfigChange(t, cs, node, mcpMCName))

	return ToCleanupFunc(t, func() {
		t.Logf("Cleaning up MCP %s", poolName)
		t.Logf("Removing label %s from node %s", MCPNameToRole(poolName), node.Name)
		unlabelFunc()

		updatedNode, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})

		node = *updatedNode

		require.NoError(t, err)
		t.Logf("Target node %s now has role(s): %v", node.Name, getNodeRoles(node))

		workerMC := GetMcName(t, cs, workerMCPName)

		// Wait for the worker pool to catch up with the deleted label
		time.Sleep(5 * time.Second)

		t.Logf("Waiting for %s pool to finish applying %s", workerMCPName, workerMC)
		require.Nil(t, WaitForPoolComplete(t, cs, workerMCPName, workerMC))

		t.Logf("Ensuring node has %s pool config before deleting pool %s", workerMCPName, poolName)
		require.Nil(t, WaitForNodeConfigChange(t, cs, node, workerMC))

		t.Logf("Deleting MCP %s", poolName)
		deleteMCPFunc()

		deleteMCs()
	})
}

func applyMCsAndWaitForRenderedConfigs(t *testing.T, cs *framework.ClientSet, poolName string, mcs []*mcfgv1.MachineConfig) (string, func()) {
	mcDeleteFuncs := []func(){}
	mcNames := []string{}

	for _, mc := range mcs {
		mcDeleteFuncs = append(mcDeleteFuncs, ApplyMC(t, cs, mc))
		mcNames = append(mcNames, mc.Name)
	}

	renderedMCName, err := WaitForRenderedConfigs(t, cs, poolName, mcNames...)
	require.NoError(t, err)

	return renderedMCName, ToCleanupFunc(t, func() {
		for i, mcDeleteFunc := range mcDeleteFuncs {
			t.Logf("Deleting MachineConfig %s", mcNames[i])
			mcDeleteFunc()
		}
	})
}

func ApplyMCToSNO(t *testing.T, cs *framework.ClientSet, mcs []*mcfgv1.MachineConfig) func() {
	mcpName := "master"

	originalMCName := GetMcName(t, cs, mcpName)

	renderedMCName, deleteMCs := applyMCsAndWaitForRenderedConfigs(t, cs, mcpName, mcs)

	require.Nil(t, WaitForSingleNodePoolComplete(t, cs, mcpName, renderedMCName))

	return ToCleanupFunc(t, func() {
		deleteMCs()
		require.NoError(t, WaitForSingleNodePoolComplete(t, cs, mcpName, originalMCName))
	})
}

func WaitForSingleNodePoolComplete(t *testing.T, cs *framework.ClientSet, pool, target string) error {
	var lastErr error
	startTime := time.Now()
	if err := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		err := WaitForPoolComplete(t, cs, pool, target)
		if err != nil {
			lastErr = err
			return false, nil
		}
		return true, nil
	}); err != nil {
		errs := kubeErrs.NewAggregate([]error{lastErr, err})
		if errors.Is(err, wait.ErrWaitTimeout) {
			return fmt.Errorf("pool %s is still not updated, waited %v: %w", pool, time.Since(startTime), errs)
		}

		return fmt.Errorf("unknown error occurred: %w", errs)
	}

	return nil
}

// GetMcName returns the current configuration name of the machine config pool poolName
func GetMcName(t *testing.T, cs *framework.ClientSet, poolName string) string {
	// grab the initial machineconfig used by the worker pool
	// this MC is gonna be the one which is going to be reapplied once the previous MC is deleted
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	require.Nil(t, err)
	return mcp.Status.Configuration.Name
}

// WaitForConfigAndPoolComplete is a helper function that gets a renderedConfig and waits for its pool to complete.
// The return value is the final rendered config.
func WaitForConfigAndPoolComplete(t *testing.T, cs *framework.ClientSet, pool, mcName string) string {
	config, err := WaitForRenderedConfig(t, cs, pool, mcName)
	require.Nil(t, err, "failed to render machine config %s from pool %s", mcName, pool)
	err = WaitForPoolComplete(t, cs, pool, config)
	require.Nil(t, err, "pool %s did not update to config %s", pool, config)
	return config
}

// WaitForRenderedConfig polls a MachineConfigPool until it has
// included the given mcName in its config, and returns the new
// rendered config name.
func WaitForRenderedConfig(t *testing.T, cs *framework.ClientSet, pool, mcName string) (string, error) {
	return WaitForRenderedConfigs(t, cs, pool, mcName)
}

// WaitForRenderedConfigs polls a MachineConfigPool until it has
// included the given mcNames in its config, and returns the new
// rendered config name.
func WaitForRenderedConfigs(t *testing.T, cs *framework.ClientSet, pool string, mcNames ...string) (string, error) {
	var renderedConfig string
	startTime := time.Now()
	found := make(map[string]bool)
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		// Set up the list
		for _, name := range mcNames {
			found[name] = false
		}

		// Update found based on the MCP
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcp.Spec.Configuration.Source {
			if _, ok := found[mc.Name]; ok {
				found[mc.Name] = true
			}
		}

		// If any are still false, then they weren't included in the MCP
		for _, nameFound := range found {
			if !nameFound {
				return false, nil
			}
		}

		// All the required names were found
		renderedConfig = mcp.Spec.Configuration.Name
		return true, nil
	}); err != nil {
		return "", fmt.Errorf("machine configs %v hasn't been picked by pool %s (waited %s): %w", notFoundNames(found), pool, time.Since(startTime), err)
	}
	t.Logf("Pool %s has rendered configs %v with %s (waited %v)", pool, mcNames, renderedConfig, time.Since(startTime))
	return renderedConfig, nil
}

func notFoundNames(foundNames map[string]bool) []string {
	out := []string{}
	for name, found := range foundNames {
		if !found {
			out = append(out, name)
		}
	}
	return out
}

// WaitForPoolComplete polls a pool until it has completed an update to target
func WaitForPoolComplete(t *testing.T, cs *framework.ClientSet, pool, target string) error {
	startTime := time.Now()

	var mcp *mcfgv1.MachineConfigPool
	var err error

	pollErr := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		mcp, err = cs.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcp.Status.Configuration.Name != target {
			return false, nil
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			return true, nil
		}
		return false, nil
	})

	if pollErr != nil {
		return fmt.Errorf("pool %s didn't report %s to updated (waited %s): %w", pool, target, time.Since(startTime), appendMCPState(mcp, pollErr))
	}

	t.Logf("Pool %s has completed %s (waited %v)", pool, target, time.Since(startTime))
	return nil
}

func appendMCPState(mcp *mcfgv1.MachineConfigPool, err error) error {
	outErrs := []error{err}

	conditionTypes := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolDegraded,
		mcfgv1.MachineConfigPoolNodeDegraded,
		mcfgv1.MachineConfigPoolRenderDegraded,
	}

	for _, conditionType := range conditionTypes {
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, conditionType) {
			condition := mcfgv1.GetMachineConfigPoolCondition(mcp.Status, conditionType)
			if condition.Message != "" || condition.Reason != "" {
				outErr := fmt.Errorf("%s: Message: %s. Reason: %s", conditionType, condition.Message, condition.Reason)
				outErrs = append(outErrs, outErr)
			}
		}
	}

	return kubeErrs.NewAggregate(outErrs)
}

func WaitForNodeConfigChange(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcName string) error {
	startTime := time.Now()
	err := wait.PollImmediate(2*time.Second, 20*time.Minute, func() (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		current := n.Annotations[constants.CurrentMachineConfigAnnotationKey]
		desired := n.Annotations[constants.DesiredMachineConfigAnnotationKey]

		state := n.Annotations[constants.MachineConfigDaemonStateAnnotationKey]

		return current == desired && desired == mcName && state == constants.MachineConfigDaemonStateDone, nil
	})

	if err != nil {
		return fmt.Errorf("node config change did not occur (waited %v): %w", time.Since(startTime), err)
	}

	t.Logf("Node %s changed config to %s (waited %v)", node.Name, mcName, time.Since(startTime))
	return nil
}

// WaitForPoolComplete polls a pool until it has completed any update
func WaitForPoolCompleteAny(t *testing.T, cs *framework.ClientSet, pool string) error {
	var mcp *mcfgv1.MachineConfigPool
	var err error

	pollErr := wait.PollImmediate(2*time.Second, 2*time.Minute, func() (bool, error) {
		mcp, err = cs.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		// wait until the cluter is back to normal (for the next test at least)
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			return true, nil
		}
		return false, nil
	})

	if pollErr != nil {
		outErr := fmt.Errorf("machine config pool did not complete an update: %w", appendMCPState(mcp, pollErr))
		t.Error(outErr)
		return outErr
	}

	return nil
}

// WaitForPausedConfig waits for configuration to be pending in a paused pool
func WaitForPausedConfig(t *testing.T, cs *framework.ClientSet, pool string) error {
	if err := wait.PollImmediate(2*time.Second, 2*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		// paused == not updated, not updating, not degraded
		if mcfgv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) &&
			mcfgv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) &&
			mcfgv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("Machine config pool never entered the state where configuration was waiting behind pause: %v", err)
	}
	return nil
}

// GetMonitoringToken retrieves the token from the openshift-monitoring secrets in the prometheus-k8s namespace.
// It is equivalent to "oc sa get-token prometheus-k8s -n openshift-monitoring"
func GetMonitoringToken(t *testing.T, cs *framework.ClientSet) (string, error) {
	token, err := cs.
		ServiceAccounts("openshift-monitoring").
		CreateToken(
			context.TODO(),
			"prometheus-k8s",
			&authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{},
			},
			metav1.CreateOptions{},
		)
	if err != nil {
		return "", fmt.Errorf("Could not request openshift-monitoring token")
	}
	return token.Status.Token, nil
}

// WaitForMCDToSyncCert waits for the MCD to write annotation on the latest controllerconfig resourceVersion,
// to indicate that is has completed the certificate write
func WaitForMCDToSyncCert(t *testing.T, cs *framework.ClientSet, node corev1.Node, resourceVersion string) error {
	if err := wait.PollImmediate(2*time.Second, 2*time.Minute, func() (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if n.Annotations[constants.ControllerConfigResourceVersionKey] == resourceVersion {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("Machine config daemon never wrote updated controllerconfig resourceVersion annotation: %v", err)
	}
	return nil
}

// ForceKubeApiserverCertificateRotation sets the kube-apiserver-to-kubelet-signer's not-after date to nil, which causes the
// apiserver to rotate it
func ForceKubeApiserverCertificateRotation(cs *framework.ClientSet) error {
	// Take note that the slash had to be encoded as ~1 because it's a reference: https://www.rfc-editor.org/rfc/rfc6901#section-3
	certPatch := fmt.Sprintf(`[{"op":"replace","path":"/metadata/annotations/auth.openshift.io~1certificate-not-after","value": null }]`)
	_, err := cs.Secrets("openshift-kube-apiserver-operator").Patch(context.TODO(), "kube-apiserver-to-kubelet-signer", types.JSONPatchType, []byte(certPatch), metav1.PatchOptions{})
	return err
}

// GetKubeletCABundleFromConfigmap fetches the latest kubelet ca bundle data from
func GetKubeletCABundleFromConfigmap(cs *framework.ClientSet) (string, error) {
	certBundle, err := cs.ConfigMaps("openshift-config-managed").Get(context.TODO(), "kube-apiserver-client-ca", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("Could not get in-cluster kube-apiserver-client-ca configmap")
	}
	if cert, ok := certBundle.Data["ca-bundle.crt"]; ok {
		return cert, nil
	}
	return "", fmt.Errorf("Could not find ca-bundle")
}

// Applies a given label to a node and returns an unlabel function.
func LabelNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, label string) func() {
	ctx := context.Background()

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		n.Labels[label] = ""
		_, err = cs.CoreV1Interface.Nodes().Update(ctx, n, metav1.UpdateOptions{})
		return err
	})

	require.Nil(t, err, "unable to label %s node %s with infra: %s", label, node.Name, err)

	return ToCleanupFunc(t, func() {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			delete(n.Labels, label)
			_, err = cs.CoreV1Interface.Nodes().Update(ctx, n, metav1.UpdateOptions{})
			return err
		})
		require.Nil(t, err, "unable to remove label %q from node %q: %s", label, node.Name, err)
	})
}

// Gets a random node from a given pool
func GetRandomNode(t *testing.T, cs *framework.ClientSet, pool string) corev1.Node {
	nodes, err := GetNodesByRole(cs, pool)
	require.Nil(t, err)
	require.NotEmpty(t, nodes)

	rand.Seed(time.Now().UnixNano())
	// Disable gosec here to avoid throwing
	// G404: Use of weak random number generator (math/rand instead of crypto/rand)
	// #nosec
	return nodes[rand.Intn(len(nodes))]
}

// LabelRandomNodeFromPool gets all nodes in pool and chooses one at random to label
func LabelRandomNodeFromPool(t *testing.T, cs *framework.ClientSet, pool, label string) func() {
	infraNode := GetRandomNode(t, cs, pool)
	return LabelNode(t, cs, infraNode, label)
}

// GetSingleNodeByRoll gets all nodes by role pool, and asserts there should only be one
func GetSingleNodeByRole(t *testing.T, cs *framework.ClientSet, role string) corev1.Node {
	nodes, err := GetNodesByRole(cs, role)
	require.Nil(t, err)
	require.Len(t, nodes, 1)
	return nodes[0]
}

// GetNodesByRole gets all nodes labeled with role role
func GetNodesByRole(cs *framework.ClientSet, role string) ([]corev1.Node, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{fmt.Sprintf("node-role.kubernetes.io/%s", role): ""}).String(),
	}
	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	return nodes.Items, nil
}

// CreateMCP create a machine config pool with name mcpName
// it will also use mcpName as the label selector, so any node you want to be included
// in the pool should have a label node-role.kubernetes.io/mcpName = ""
func CreateMCP(t *testing.T, cs *framework.ClientSet, mcpName string) func() {
	infraMCP := &mcfgv1.MachineConfigPool{}
	infraMCP.Name = mcpName
	nodeSelector := metav1.LabelSelector{}
	infraMCP.Spec.NodeSelector = &nodeSelector
	infraMCP.Spec.NodeSelector.MatchLabels = make(map[string]string)
	infraMCP.Spec.NodeSelector.MatchLabels[MCPNameToRole(mcpName)] = ""
	mcSelector := metav1.LabelSelector{}
	infraMCP.Spec.MachineConfigSelector = &mcSelector
	infraMCP.Spec.MachineConfigSelector.MatchExpressions = []metav1.LabelSelectorRequirement{
		{
			Key:      mcfgv1.MachineConfigRoleLabelKey,
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"worker", mcpName},
		},
	}
	infraMCP.ObjectMeta.Labels = make(map[string]string)
	infraMCP.ObjectMeta.Labels[mcpName] = ""
	_, err := cs.MachineConfigPools().Create(context.TODO(), infraMCP, metav1.CreateOptions{})
	require.Nil(t, err)
	return ToCleanupFunc(t, func() {
		err := cs.MachineConfigPools().Delete(context.TODO(), mcpName, metav1.DeleteOptions{})
		require.Nil(t, err)
	})
}

type SSHPaths struct {
	// The path where SSH keys are expected to be found.
	Expected string
	// The path where SSH keys are *not* expected to be found.
	NotExpected string
}

// Determines where to expect SSH keys for the core user on a given node based upon the node's OS.
func GetSSHPaths(os osrelease.OperatingSystem) SSHPaths {
	if os.IsEL9() || os.IsSCOS() || os.IsFCOS() {
		return SSHPaths{
			Expected:    constants.RHCOS9SSHKeyPath,
			NotExpected: constants.RHCOS8SSHKeyPath,
		}
	}

	return SSHPaths{
		Expected:    constants.RHCOS8SSHKeyPath,
		NotExpected: constants.RHCOS9SSHKeyPath,
	}
}

// MCPNameToRole converts a mcpName to a node role label
func MCPNameToRole(mcpName string) string {
	return fmt.Sprintf("node-role.kubernetes.io/%s", mcpName)
}

// CreateMC creates a machine config object with name and role
func CreateMC(name, role string) *mcfgv1.MachineConfig {
	return NewMachineConfig(name, MCLabelForRole(role), "", nil)
}

func AssertFileOnNodeEmpty(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	t.Helper()

	if !AssertFileOnNode(t, cs, node, path) {
		return false
	}

	contents := getFileContents(t, cs, node, path)
	return assert.Empty(t, contents, "expected file %q on %q to be empty: got %q", path, node.Name, contents)
}

func AssertFileOnNodeNotEmpty(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	t.Helper()

	if !AssertFileOnNode(t, cs, node, path) {
		return false
	}

	contents := getFileContents(t, cs, node, path)
	return assert.NotEmpty(t, contents, "expected file %q on %q to not to be empty: got %q", path, node.Name, contents)
}

func AssertFileOnNodeContains(t *testing.T, cs *framework.ClientSet, node corev1.Node, path, expectedContents string) bool {
	t.Helper()

	exists := AssertFileOnNode(t, cs, node, path)
	if !exists {
		return false
	}

	contents := getFileContents(t, cs, node, path)
	return assert.Contains(t, contents, expectedContents, "expected file %q on %q to contain %q: got %q", path, node.Name, expectedContents, contents)
}

func AssertFileOnNodeNotContains(t *testing.T, cs *framework.ClientSet, node corev1.Node, path, unexpectedContents string) bool {
	t.Helper()

	exists := AssertFileOnNode(t, cs, node, path)
	if !exists {
		return false
	}

	contents := getFileContents(t, cs, node, path)
	return assert.NotContains(t, contents, unexpectedContents, "expected file %q on %q not to contain %q: got %q", path, node.Name, unexpectedContents, contents)
}

// Asserts that a given file is present on the underlying node.
func AssertFileOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	t.Helper()

	path = canonicalizeNodeFilePath(path)

	out, err := ExecCmdOnNodeWithError(cs, node, "stat", path)

	return assert.NoError(t, err, "expected to find file %s on %s, got:\n%s\nError: %s", path, node.Name, out, err)
}

// Asserts that a given file is *not* present on the underlying node.
func AssertFileNotOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	t.Helper()

	path = canonicalizeNodeFilePath(path)

	out, err := ExecCmdOnNodeWithError(cs, node, "stat", path)

	return assert.Error(t, err, "expected not to find file %s on %s, got:\n%s", path, node.Name, out) &&
		assert.Contains(t, out, "No such file or directory", "expected command output to contain 'No such file or directory', got: %s", out)
}

// Adds the /rootfs onto a given file path, if not already present.
func canonicalizeNodeFilePath(path string) string {
	rootfs := "/rootfs"

	if !strings.HasPrefix(path, rootfs) {
		return filepath.Join(rootfs, path)
	}

	return path
}

func getFileContents(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) string {
	return ExecCmdOnNode(t, cs, node, "cat", canonicalizeNodeFilePath(path))
}

// ExecCmdOnNode finds a node's mcd, and oc rsh's into it to execute a command on the node
// all commands should use /rootfs as root
func ExecCmdOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, subArgs ...string) string {
	t.Helper()

	cmd, err := execCmdOnNode(cs, node, subArgs...)
	require.Nil(t, err, "could not prepare to exec cmd %v on node %s: %s", subArgs, node.Name, err)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	require.Nil(t, err, "failed to exec cmd %v on node %s: %s", subArgs, node.Name, string(out))
	return string(out)
}

// ExecCmdOnNodeWithError behaves like ExecCmdOnNode, with the exception that
// any errors are returned to the caller for inspection. This allows one to
// execute a command that is expected to fail; e.g., stat /nonexistant/file.
func ExecCmdOnNodeWithError(cs *framework.ClientSet, node corev1.Node, subArgs ...string) (string, error) {
	cmd, err := execCmdOnNode(cs, node, subArgs...)
	if err != nil {
		return "", err
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("failed to exec cmd %v on node %s: %s: %w", subArgs, node.Name, string(out), err)
	}

	return string(out), err
}

// ExecCmdOnNode finds a node's mcd, and oc rsh's into it to execute a command on the node
// all commands should use /rootfs as root
func execCmdOnNode(cs *framework.ClientSet, node corev1.Node, subArgs ...string) (*exec.Cmd, error) {
	// Check for an oc binary in $PATH.
	path, err := exec.LookPath("oc")
	if err != nil {
		return nil, fmt.Errorf("could not locate oc command: %w", err)
	}

	// Get the kubeconfig file path
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return nil, fmt.Errorf("could not get kubeconfig: %w", err)
	}

	mcd, err := mcdForNode(cs, &node)
	if err != nil {
		return nil, fmt.Errorf("could not get MCD for node %s: %w", node.Name, err)
	}

	mcdName := mcd.ObjectMeta.Name

	entryPoint := path
	args := []string{"rsh",
		"-n", "openshift-machine-config-operator",
		"-c", "machine-config-daemon",
		mcdName}
	args = append(args, subArgs...)

	cmd := exec.Command(entryPoint, args...)
	// If one passes a path to a kubeconfig via NewClientSet instead of setting
	// $KUBECONFIG, oc will be unaware of it. To remedy, we explicitly set
	// KUBECONFIG to the value held by the clientset.
	cmd.Env = append(cmd.Env, "KUBECONFIG="+kubeconfig)
	return cmd, nil
}

// IsOKDCluster checks whether the Upstream field on the CV spec references an OKD release controller
func IsOKDCluster(cs *framework.ClientSet) (bool, error) {
	cv, err := cs.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	// TODO: Adjust this as OKD becomes available for different platforms, e.g., arm64.
	okdReleaseControllers := sets.NewString("https://amd64.origin.releases.ci.openshift.org/graph")
	return okdReleaseControllers.Has(string(cv.Spec.Upstream)), nil
}

func MCLabelForRole(role string) map[string]string {
	mcLabels := make(map[string]string)
	mcLabels[mcfgv1.MachineConfigRoleLabelKey] = role
	return mcLabels
}

func MCLabelForWorkers() map[string]string {
	return MCLabelForRole("worker")
}

// TODO consider also testing for Ign2
// func createIgn2File(path, content, fs string, mode int) ign2types.File {
// 	return ign2types.File{
// 		FileEmbedded1: ign2types.FileEmbedded1{
// 			Contents: ign2types.FileContents{
// 				Source: content,
// 			},
// 			Mode: &mode,
// 		},
// 		Node: ign2types.Node{
// 			Filesystem: fs,
// 			Path:       path,
// 			User: &ign2types.NodeUser{
// 				Name: "root",
// 			},
// 		},
// 	}
// }

// Creates an Ign3 file whose contents are gzipped and encoded according to
// https://datatracker.ietf.org/doc/html/rfc2397
func CreateGzippedIgn3File(path, content string, mode int) (ign3types.File, error) {
	ign3File := ign3types.File{}

	buf := bytes.NewBuffer([]byte{})

	gzipWriter := gzip.NewWriter(buf)
	if _, err := gzipWriter.Write([]byte(content)); err != nil {
		return ign3File, err
	}

	if err := gzipWriter.Close(); err != nil {
		return ign3File, err
	}

	ign3File = CreateEncodedIgn3File(path, buf.String(), mode)
	ign3File.Contents.Compression = StrToPtr("gzip")

	return ign3File, nil
}

// Creates an Ign3 file whose contents are encoded according to
// https://datatracker.ietf.org/doc/html/rfc2397
func CreateEncodedIgn3File(path, content string, mode int) ign3types.File {
	encoded := dataurl.EncodeBytes([]byte(content))

	return CreateIgn3File(path, encoded, mode)
}

func CreateIgn3File(path, content string, mode int) ign3types.File {
	return ign3types.File{
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: &content,
			},
			Mode: &mode,
		},
		Node: ign3types.Node{
			Path: path,
			User: ign3types.NodeUser{
				Name: StrToPtr("root"),
			},
		},
	}
}

func MCDForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	return mcdForNode(cs, node)
}

// Returns the node's uptime expressed as the total number of seconds the
// system has been up.
// See: https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/6/html/deployment_guide/s2-proc-uptime
func GetNodeUptime(t *testing.T, cs *framework.ClientSet, node corev1.Node) float64 {
	output := ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/uptime")
	uptimeString := strings.Split(output, " ")[0]
	uptime, err := strconv.ParseFloat(uptimeString, 64)
	require.Nil(t, err)
	return uptime
}

// Asserts that a node has rebooted, given an initial uptime value.
//
// To Use:
//
// initialUptime := GetNodeUptime(t, cs, node)
// // do stuff that should cause a reboot
// AssertNodeReboot(t, cs, node, initialUptime)
//
// Returns the result of the assertion as is the convention for the testify/assert
func AssertNodeReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node, initialUptime float64) bool {
	current := GetNodeUptime(t, cs, node)
	return assert.Greater(t, initialUptime, current, "node %s did not reboot as expected; initial uptime: %d, current uptime: %d", node.Name, initialUptime, current)
}

// Asserts that a node has not rebooted, given an initial uptime value.
//
// To Use:
//
// initialUptime := GetNodeUptime(t, cs, node)
// // do stuff that should not cause a reboot
// AssertNodeNotReboot(t, cs, node, initialUptime)
//
// Returns the result of the assertion as is the convention for the testify/assert
func AssertNodeNotReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node, initialUptime float64) bool {
	current := GetNodeUptime(t, cs, node)
	return assert.Greater(t, current, initialUptime, "node %s rebooted unexpectedly; initial uptime: %d, current uptime: %d", node.Name, initialUptime, current)
}

// Overrides a global path variable by appending its original value onto a
// tempdir created by t.TempDir(). Returns a function that reverts it back. If
// you wanted to override the origParentDirPath, you would do something like
// this:
//
// cleanupFunc := OverrideGlobalPathVar(t, "origParentDirPath", &origParentDirPath)
// defer cleanupFunc()
//
// NOTE: testing.T will clean up any tempdirs it creates after each test, so
// there is no need to do something like defer os.RemoveAll().
func OverrideGlobalPathVar(t *testing.T, name string, val *string) func() {
	oldValue := *val

	newValue := filepath.Join(t.TempDir(), oldValue)

	*val = newValue

	t.Logf("original value of %s (%s) overridden with %s", name, oldValue, newValue)

	return ToCleanupFunc(t, func() {
		t.Logf("original value of %s restored to %s", name, oldValue)
		*val = oldValue
	})
}

type NodeOSRelease struct {
	// The contents of the /etc/os-release file
	EtcContent string
	// The contents of the /usr/lib/os-release file
	LibContent string
	// The parsed contents
	OS osrelease.OperatingSystem
}

// Retrieves the /etc/os-release and /usr/lib/os-release file contents on a
// given node and parses it through the OperatingSystem code.
func GetOSReleaseForNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) NodeOSRelease {
	etcOSReleaseContent := ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", osrelease.EtcOSReleasePath))
	libOSReleaseContent := ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", osrelease.LibOSReleasePath))

	os, err := osrelease.LoadOSRelease(etcOSReleaseContent, libOSReleaseContent)
	require.NoError(t, err)

	return NodeOSRelease{
		EtcContent: etcOSReleaseContent,
		LibContent: libOSReleaseContent,
		OS:         os,
	}
}

func mcdForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	// find the MCD pod that has spec.nodeNAME = node.Name and get its name:
	listOptions := metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}).String(),
	}
	listOptions.LabelSelector = labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String()

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	if len(mcdList.Items) != 1 {
		if len(mcdList.Items) == 0 {
			return nil, fmt.Errorf("failed to find MCD for node %s", node.Name)
		}
		return nil, fmt.Errorf("too many (%d) MCDs for node %s", len(mcdList.Items), node.Name)
	}
	return &mcdList.Items[0], nil
}

func getNodeRoles(node corev1.Node) []string {
	nodeRoles := []string{}

	for labelKey := range node.Labels {
		if strings.Contains(labelKey, "node-role.kubernetes.io") {
			nodeRoles = append(nodeRoles, labelKey)
		}
	}

	return nodeRoles
}
