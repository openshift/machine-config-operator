package helpers

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

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

	return func() {
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}))
	}
}

func CreatePoolAndApplyMC(t *testing.T, cs *framework.ClientSet, poolName string, mc *mcfgv1.MachineConfig) func() {
	workerMCPName := "worker"

	t.Logf("Setting up pool %s", poolName)

	unlabelFunc := LabelRandomNodeFromPool(t, cs, workerMCPName, MCPNameToRole(poolName))
	deleteMCPFunc := CreateMCP(t, cs, poolName)

	node := GetSingleNodeByRole(t, cs, poolName)

	t.Logf("Target Node: %s", node.Name)

	mcDeleteFunc := ApplyMC(t, cs, mc)

	WaitForConfigAndPoolComplete(t, cs, poolName, mc.Name)

	mcpMCName := GetMcName(t, cs, poolName)
	require.Nil(t, WaitForNodeConfigChange(t, cs, node, mcpMCName))

	return func() {
		t.Logf("Cleaning up MCP %s", poolName)
		t.Logf("Removing label %s from node %s", MCPNameToRole(poolName), node.Name)
		unlabelFunc()

		workerMC := GetMcName(t, cs, workerMCPName)

		// Wait for the worker pool to catch up with the deleted label
		time.Sleep(5 * time.Second)

		t.Logf("Waiting for %s pool to finish applying %s", workerMCPName, workerMC)
		require.Nil(t, WaitForPoolComplete(t, cs, workerMCPName, workerMC))

		t.Logf("Ensuring node has %s pool config before deleting pool %s", workerMCPName, poolName)
		require.Nil(t, WaitForNodeConfigChange(t, cs, node, workerMC))

		t.Logf("Deleting MCP %s", poolName)
		deleteMCPFunc()

		t.Logf("Deleting MachineConfig %s", mc.Name)
		mcDeleteFunc()
	}
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
		return "", errors.Wrapf(err, "machine configs %v hasn't been picked by pool %s (waited %s)", notFoundNames(found), pool, time.Since(startTime))
	}
	t.Logf("Pool %s has rendered configs %v with %s (waited %v)", pool, mcNames, renderedConfig, time.Since(startTime))
	return renderedConfig, nil
}

func WaitForBuild(t *testing.T, cs *framework.ClientSet, build string) {
	startTime := time.Now()
	if err := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		build, err := cs.BuildV1Interface.Builds("openshift-machine-config-operator").Get(context.TODO(), build, metav1.GetOptions{})
		require.Nil(t, err)
		if build.Status.Phase == "Complete" {
			return true, nil
		}
		require.NotContains(t, []string{"Failed", "Error", "Cancelled"}, build.Status.Phase)
		return false, nil
	}); err != nil {
		require.Nil(t, err, "build %q did not complete (waited %s)", build, time.Since(startTime))
	}
	t.Logf("build %q has completed (waited %s)", build, time.Since(startTime))
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
	if err := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
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
	}); err != nil {
		return errors.Wrapf(err, "pool %s didn't report %s to updated (waited %s)", pool, target, time.Since(startTime))
	}
	t.Logf("Pool %s has completed %s (waited %v)", pool, target, time.Since(startTime))
	return nil
}

func WaitForNodeConfigChange(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcName string) error {
	startTime := time.Now()
	err := wait.PollImmediate(2*time.Second, 20*time.Minute, func() (bool, error) {
		n, err := cs.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
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

// LabelRandomNodeFromPool gets all nodes in pool and chooses one at random to label
func LabelRandomNodeFromPool(t *testing.T, cs *framework.ClientSet, pool, label string) func() {
	nodes, err := GetNodesByRole(cs, pool)
	require.Nil(t, err)
	require.NotEmpty(t, nodes)

	rand.Seed(time.Now().UnixNano())
	// Disable gosec here to avoid throwing
	// G404: Use of weak random number generator (math/rand instead of crypto/rand)
	// #nosec
	infraNode := nodes[rand.Intn(len(nodes))]
	infraNodeName := infraNode.Name

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		infraNode, err := cs.Nodes().Get(context.TODO(), infraNodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		infraNode.Labels[label] = ""
		_, err = cs.Nodes().Update(context.TODO(), infraNode, metav1.UpdateOptions{})
		return err
	})
	require.Nil(t, err, "unable to label worker node %s with infra: %s", infraNode.Name, err)

	return func() {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			infraNode, err := cs.Nodes().Get(context.TODO(), infraNodeName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			delete(infraNode.Labels, label)
			_, err = cs.Nodes().Update(context.TODO(), infraNode, metav1.UpdateOptions{})
			return err
		})
		require.Nil(t, err, "unable to remove label from node %s: %s", infraNode.Name, err)
	}
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
	nodes, err := cs.Nodes().List(context.TODO(), listOptions)
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
	return func() {
		err := cs.MachineConfigPools().Delete(context.TODO(), mcpName, metav1.DeleteOptions{})
		require.Nil(t, err)
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

// ExecCmdOnNode finds a node's mcd, and oc rsh's into it to execute a command on the node
// all commands should use /rootfs as root
func ExecCmdOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, subArgs ...string) string {
	// Check for an oc binary in $PATH.
	path, err := exec.LookPath("oc")
	if err != nil {
		t.Fatalf("could not locate oc command: %s", err)
	}

	// Get the kubeconfig file path
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		t.Fatalf("could not get kubeconfig: %s", err)
	}

	mcd, err := mcdForNode(cs, &node)
	require.Nil(t, err)
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
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	require.Nil(t, err, "failed to exec cmd %v on node %s: %s", subArgs, node.Name, string(out))
	return string(out)
}

// WriteToNode finds a node's mcd and writes a file over oc rsh's stdin
// filename should include /rootfs to write to node filesystem
func WriteToMCDContainer(t *testing.T, cs *framework.ClientSet, node corev1.Node, filename string, data []byte) {
	mcd, err := mcdForNode(cs, &node)
	require.Nil(t, err)
	mcdName := mcd.ObjectMeta.Name

	entryPoint := "oc"
	args := []string{"rsh",
		"-n", "openshift-machine-config-operator",
		"-c", "machine-config-daemon",
		mcdName,
		"tee", filename,
	}

	cmd := exec.Command(entryPoint, args...)
	cmd.Stderr = os.Stderr
	cmd.Stdin = bytes.NewReader(data)

	out, err := cmd.Output()
	require.Nil(t, err, "failed to write data to file %q on node %s: %s", filename, node.Name, string(out))
}

// RebootAndWait reboots a node and then waits until the node has rebooted and its status is again Ready
func RebootAndWait(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	updatedNode, err := cs.Nodes().Get(context.TODO(), node.ObjectMeta.Name, metav1.GetOptions{})
	require.Nil(t, err)
	prevBootID := updatedNode.Status.NodeInfo.BootID
	ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "systemctl", "reboot")
	startTime := time.Now()
	if err := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(context.TODO(), node.ObjectMeta.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Status.NodeInfo.BootID != prevBootID {
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == "True" {
					return true, nil
				}
			}
		}
		return false, nil
	}); err != nil {
		require.Nil(t, err, "node %q never rebooted (waited %s)", node.ObjectMeta.Name, time.Since(startTime))
	}
	t.Logf("node %q has rebooted (waited %s)", node.ObjectMeta.Name, time.Since(startTime))
}

// IsOKDCluster checks whether the Upstream field on the CV spec references OKD's update server
func IsOKDCluster(cs *framework.ClientSet) (bool, error) {
	cv, err := cs.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if cv.Spec.Upstream == "https://origin-release.svc.ci.openshift.org/graph" {
		return true, nil
	}
	return false, nil
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
