package helpers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	mcdContainerName string = "machine-config-daemon"
	mcdDaemonsetName string = mcdContainerName
)

// Provides a way to execute commands on a given node in a variety of contexts.
type NodeExec struct {
	mcdPod            *corev1.Pod
	cs                *framework.ClientSet
	t                 *testing.T
	sanitizedTestName string
	kubeconfigPath    string
	ocPath            string
	verbose           bool
	refreshMCDPod     bool
}

type CopyOpts struct {
	Local  string
	Remote string
}

func (c *CopyOpts) validate() error {
	if strings.Contains(c.Remote, "/rootfs") {
		return fmt.Errorf("cannot contain path fragment /rootfs")
	}

	return nil
}

func (c *CopyOpts) getCanonicalRemotePath() string {
	return filepath.Join("/rootfs", c.Remote)
}

type ScriptOpts struct {
	Contents       string
	RunCleanup     bool
	SeparateOutput bool
	RefreshMCDPod  bool
}

type ExecOpts struct {
	Command        []string
	SeparateOutput bool
	RefreshMCDPod  bool
}

func (e *ExecOpts) validate() error {
	if len(e.Command) == 0 {
		return fmt.Errorf("command must be provided")
	}

	for _, item := range e.Command {
		if item == "" {
			return fmt.Errorf("empty args not allowed")
		}
	}

	invalids := []string{
		"/rootfs",
		"chroot",
	}

	argsString := strings.Join(e.Command, " ")
	for _, invalid := range invalids {
		if strings.Contains(argsString, invalid) {
			return fmt.Errorf("args must not contain %s", invalid)
		}
	}

	return nil
}

type Results struct {
	Combined    *bytes.Buffer
	Stdout      *bytes.Buffer
	Stderr      *bytes.Buffer
	OriginalCmd []string
	OcCmd       []string
}

type ocOpts struct {
	ocArgs  []string
	cmdArgs []string
}

func (o *ocOpts) getCommandArgs(ocPath string) []string {
	args := []string{}

	if o.ocArgs[0] == "oc" || o.ocArgs[0] == ocPath {
		args = o.ocArgs[1:]
	}

	if len(o.cmdArgs) == 0 {
		return args
	}

	return append(args, o.cmdArgs...)
}

func (o *ocOpts) getResults(ocPath string) *Results {
	withOCPath := []string{}

	if o.ocArgs[0] != "oc" && o.ocArgs[0] != ocPath {
		withOCPath = append([]string{ocPath}, o.ocArgs...)
	} else {
		withOCPath = o.ocArgs
	}

	return &Results{
		OriginalCmd: o.cmdArgs,
		OcCmd:       withOCPath,
	}
}

// The options to provide. Note: Only the MCDPod or the Node may be provided; not both.
type NodeExecOpts struct {
	// The MCD pod to use as a bastion.
	MCDPod *corev1.Pod
	// The node to target.
	Node *corev1.Node
	// Be verbose about logging.
	Verbose bool
	// Refresh MCD pod before executing a command or script.
	RefreshMCDPod bool
}

// Determines if the options are valid.
func (n *NodeExecOpts) validate() error {
	if n.MCDPod == nil && n.Node == nil {
		return fmt.Errorf("either MCDPod or Node must be set")
	}

	if n.MCDPod != nil && n.Node != nil {
		return fmt.Errorf("either MCDPod or Node must be set, not both")
	}

	if n.MCDPod != nil {
		ls := labels.SelectorFromSet(labels.Set{"k8s-app": mcdDaemonsetName})
		if !ls.Matches(labels.Set(n.MCDPod.Labels)) {
			return fmt.Errorf("not an MCD pod, missing %s label", ls.String())
		}
	}

	return nil
}

// Resolves the MCD pod. When provided, it just returns a deep copy of the MCD
// pod object. When not provided, it uses the mcdForNode() function to look up
// the MCD for that node.
func (n *NodeExecOpts) resolveMCDPod(cs *framework.ClientSet) (*corev1.Pod, error) {
	if n.MCDPod != nil {
		return n.MCDPod.DeepCopy(), nil
	}

	mcdPod, err := mcdForNode(cs, n.Node)
	if err != nil {
		return nil, err
	}

	return mcdPod, nil
}

// Constructs a NodeExec object or fails the test.
func NewNodeExecOrFail(t *testing.T, cs *framework.ClientSet, opts *NodeExecOpts) *NodeExec {
	ne, err := NewNodeExec(t, cs, opts)
	require.NoError(t, err)
	return ne
}

func NewNodeExecForNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) (*NodeExec, error) {
	return NewNodeExec(t, cs, &NodeExecOpts{
		Node: &node,
	})
}

// Constructs a NodeExec object.
func NewNodeExec(t *testing.T, cs *framework.ClientSet, opts *NodeExecOpts) (*NodeExec, error) {
	// Ensure our options are valid
	if err := opts.validate(); err != nil {
		return nil, err
	}

	// Ensure that we have the oc binary
	ocPath, err := exec.LookPath("oc")
	if err != nil {
		return nil, fmt.Errorf("required oc binary not found: %w", err)
	}

	// Get the kubeconfig from the clientset to explicitly pass into oc.
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return nil, err
	}

	// Gets the MCD pod.
	mcdPod, err := opts.resolveMCDPod(cs)
	if err != nil {
		return nil, err
	}

	ne := &NodeExec{
		cs:                cs,
		t:                 t,
		sanitizedTestName: sanitizeTestName(t.Name()),
		kubeconfigPath:    kubeconfig,
		mcdPod:            mcdPod,
		ocPath:            ocPath,
		verbose:           opts.Verbose,
		refreshMCDPod:     opts.RefreshMCDPod,
	}

	return ne, nil
}

func (n *NodeExec) maybeLog(format string, a ...any) {
	if n.verbose {
		n.t.Logf(format, a...)
	}
}

func (n *NodeExec) ExecuteCommand(opts ExecOpts) (*Results, error) {
	n.maybeLog("Executing %q on node %s", strings.Join(opts.Command, " "), n.nodeName())

	if err := opts.validate(); err != nil {
		return nil, err
	}

	if opts.RefreshMCDPod {
		if err := n.refreshMCDPodFromAPI(); err != nil {
			return nil, err
		}
	}

	return n.runOC(opts.SeparateOutput, ocOpts{
		ocArgs: []string{
			"oc",
			"rsh",
			"-n", ctrlcommon.MCONamespace,
			"-c", mcdContainerName,
			n.mcdPod.Name,
			"chroot",
			"/rootfs",
		},
		cmdArgs: opts.Command,
	})
}

// Writes a script to the node and executes it, returning a cleanup function
// which the caller is responsible for calling.
// 1. Writes the script to the local file system in a temp directory.
// 2. Creates a temp directory on the node to copy the script to.
// 3. Copies the script to the remote temp directory on the node.
// 4. Makes the script executable on the node (chmod +x).
// 5. Executes the script.
func (n *NodeExec) ExecuteScript(opts ScriptOpts) (*Results, func() error, error) {
	scriptFilename := "node-exec-script"

	n.maybeLog("Executing script on node %s", n.nodeName())

	remoteTmpDir, remoteCleanupFunc, err := n.CreateRemoteTempDir()
	if err != nil {
		return nil, nil, err
	}

	localTmpDir, localCleanupFunc, err := n.createLocalTempDir()
	if err != nil {
		return nil, nil, err
	}

	localFilename := filepath.Join(localTmpDir, scriptFilename)
	remoteFilename := filepath.Join(remoteTmpDir, scriptFilename)

	if err := os.WriteFile(localFilename, []byte(opts.Contents), 0o755); err != nil {
		return nil, nil, err
	}

	copyOpts := CopyOpts{
		Local:  localFilename,
		Remote: remoteFilename,
	}

	if err := n.CopyToNode(copyOpts); err != nil {
		return nil, nil, err
	}

	if err := localCleanupFunc(); err != nil {
		return nil, nil, err
	}

	chmodOpts := ExecOpts{
		Command: []string{"chmod", "+x", remoteFilename},
	}

	if _, err := n.ExecuteCommand(chmodOpts); err != nil {
		return nil, nil, err
	}

	scriptExecOpts := ExecOpts{
		Command:        []string{remoteFilename},
		SeparateOutput: opts.SeparateOutput,
	}

	out, err := n.ExecuteCommand(scriptExecOpts)
	if !opts.RunCleanup {
		// The caller is responsible for checking the error and any output to
		// determine what to do next.
		return out, remoteCleanupFunc, err
	}

	if err != nil {
		return nil, nil, err
	}

	if err := remoteCleanupFunc(); err != nil {
		return nil, nil, err
	}

	return out, nil, nil
}

// Stages and then copies files to the node to the given remote directory.
func (n *NodeExec) CopyFilesToNode(localFiles []string, remote string) error {
	localTempDir, cleanupFunc, err := n.createLocalTempDir()
	if err != nil {
		return err
	}

	defer cleanupFunc()

	// First, copy the files into a local staging location.
	for _, file := range localFiles {
		cmd := exec.Command("cp", file, filepath.Join(localTempDir, filepath.Base(file)))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to copy file %s to %s: %w", file, localTempDir, err)
		}
	}

	// Next, copy the local staging location to the node. Under the hood, oc cp
	// will invoke tar to archive the directory and to untar it on the other
	// side. We need to append "/." onto the local temp directory so that oc will
	// unpack it into the remote directory.
	copyOpts := CopyOpts{
		Local:  localTempDir + "/.",
		Remote: remote,
	}

	if err := n.CopyToNode(copyOpts); err != nil {
		return err
	}

	return cleanupFunc()
}

// Copies a given local file onto the node at its path.
func (n *NodeExec) CopyToNode(opts CopyOpts) error {
	if err := opts.validate(); err != nil {
		return err
	}

	mcdPodNameAndPath := fmt.Sprintf("%s:%s", n.mcdPod.Name, opts.getCanonicalRemotePath())

	oco := ocOpts{
		ocArgs: []string{
			"oc",
			"cp",
			opts.Local,
			mcdPodNameAndPath,
			"-c", mcdContainerName,
			"-n", ctrlcommon.MCONamespace,
		},
	}

	results, err := n.runOC(true, oco)
	if err != nil {
		return fmt.Errorf("could not copy local file %q to remote %q, (resolved as %q), got %s: %w", opts.Local, opts.Remote, mcdPodNameAndPath, results.Combined.String(), err)
	}

	n.maybeLog("Copied local file %q to path %q on node %s", opts.Local, opts.Remote, n.nodeName())

	return nil
}

// Copies a given file on the node to the provided local path.
func (n *NodeExec) CopyFromNode(opts CopyOpts) error {
	mcdPodNameAndPath := fmt.Sprintf("%s/%s:%s", ctrlcommon.MCONamespace, n.mcdPod.Name, opts.getCanonicalRemotePath())

	oco := ocOpts{
		ocArgs: []string{
			"oc",
			"cp",
			"-c", "machine-config-daemon",
			mcdPodNameAndPath,
			opts.Local,
		},
	}

	results, err := n.runOC(true, oco)
	if err != nil {
		return fmt.Errorf("could not copy remote file %q (resolved as %q) to local %q, got %s: %w", opts.Remote, mcdPodNameAndPath, opts.Local, results.Combined.String(), err)
	}

	// This is needed because oc cp shells out to tar for remote file copies.
	// When this happens and fails because the file does not exist, oc eats the
	// return code from the tar command.
	if _, err := os.Stat(opts.Local); err != nil {
		return fmt.Errorf("could not copy remote file %q (resolved as %q) to local %q: %w", opts.Remote, mcdPodNameAndPath, opts.Local, err)
	}

	n.maybeLog("Copied remote file %q from node %s to local dir %q", opts.Remote, n.nodeName(), opts.Local)
	return nil
}

// Creates a remote temporary directory on the node and returns a cleanup
// function to remove it. Note: It is the callers' responsibility to call the
// cleanup function; it will not be done automatically.
func (n *NodeExec) CreateRemoteTempDir() (string, func() error, error) {
	results, err := n.ExecuteCommand(ExecOpts{
		Command: []string{"mktemp", "-d", "--suffix", n.sanitizedTestName},
	})

	if err != nil {
		return "", nil, err
	}

	remoteTmpDir := strings.TrimSpace(results.Combined.String())
	n.maybeLog("Created temp dir %q on node %s", remoteTmpDir, n.nodeName())

	if remoteTmpDir == "" {
		return "", nil, fmt.Errorf("got an error while trying to create remote temp dir: %s", results.Combined.String())
	}

	cleanupFunc := MakeIdempotentWithError(func() error {
		n.maybeLog("Cleaning up %s on node %s", remoteTmpDir, n.nodeName())
		_, err := n.ExecuteCommand(ExecOpts{
			Command: []string{"rm", "-rf"},
		})
		return err
	})

	return remoteTmpDir, cleanupFunc, nil
}

// Creates a local temp directory and returns a cleanup function. Note: It is
// the callers responsibility to call the cleanup function; it will not be done
// automatically.
func (n *NodeExec) createLocalTempDir() (string, func() error, error) {
	tmpDir, err := os.MkdirTemp("", n.sanitizedTestName)
	if err != nil {
		return "", nil, err
	}

	cleanupFunc := func() error {
		return os.RemoveAll(tmpDir)
	}

	return tmpDir, cleanupFunc, nil
}

// Fetches a newer version of the MCD pod.
func (n *NodeExec) refreshMCDPodFromAPI() error {
	// If the MCD pod is still present, that means we only have to fetch the latest version of it.
	pod, err := n.cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Get(context.TODO(), n.mcdPod.Name, metav1.GetOptions{})
	// For any errors other than not found, we return.
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	if err == nil {
		n.mcdPod = pod
		return nil
	}

	// At this point, we know that the MCD pod was not found. So lets look it up
	// by node name.
	mcdPod, err := mcdForNodeName(n.cs, n.nodeName())
	if err != nil {
		return err
	}

	n.mcdPod = mcdPod
	n.maybeLog("Refreshed MCD pod, using %s for node %s", mcdPod.Name, n.nodeName())
	return nil
}

// Runs the desired oc command without collectiong any output; except in cases of error.
func (n *NodeExec) runOC(separateOutput bool, opts ocOpts) (*Results, error) {
	args := opts.getCommandArgs(n.ocPath)
	cmd := exec.Command(n.ocPath, args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", n.kubeconfigPath))

	n.maybeLog("Running oc command: %q", cmd.String())

	results := opts.getResults(n.ocPath)

	if !separateOutput {
		out, err := cmd.CombinedOutput()
		results.Combined = bytes.NewBuffer(out)
		return results, err
	}

	stdout := bytes.NewBuffer([]byte{})
	stderr := bytes.NewBuffer([]byte{})
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	results.Stdout = stdout
	results.Stderr = stderr
	return results, err
}

// Gets the target node name.
func (n *NodeExec) nodeName() string {
	return n.mcdPod.Spec.NodeName
}

// Sanitizes the provided test name so that it is suitable to be used as a path fragment.
func sanitizeTestName(name string) string {
	invalids := []string{
		":",
		"/",
		" ",
		"'",
		"\"",
	}

	for _, invalid := range invalids {
		name = strings.ReplaceAll(name, invalid, "_")
	}

	return name
}
