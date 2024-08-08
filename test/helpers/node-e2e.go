package helpers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/clarketm/json"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// The name of the MachineConfig file that is written to disk and then copied to the node.
	machineconfigName string = "machineconfig.json"
	// The name of the ControllerConfig file that is written to disk and then copied to the node.
	ctrlcfgName string = "controllerconfig.json"
)

// Holds all of the configurable options for a NodeE2E test. This type of test
// is where a standard Golang test is compiled locally (or within the test pod)
// and copied to a given node, where it is run natively. The results are
// collected and optionall parsed into junit-compliant output.
type NodeE2ETestOpts struct {
	// The test names to run.
	TestNames []string
	// The node to target for the test run.
	Node corev1.Node
	// The test binaries which can be used for the test (see testBinary comments for more info).
	TestBinaries []*TestBinary
	// Collect the output as a standard test output log.
	CollectOutput bool
	// Collect the output as a junit test output log (requires the go.junit-report binary to be present).
	CollectOutputAsJunit bool
	// What directory to write the test output to.
	OutputDir string
}

// To run a Node E2E test, pass NodeE2ETestOpts and the clientset into this function and call it.
func RunE2ETestsOnNode(t *testing.T, cs *framework.ClientSet, testOpts NodeE2ETestOpts) {
	t.Helper()
	require.NoError(t, testOpts.run(t, cs))
}

// The main entrypoint into Node E2E tests.
// In general, the way this works is:
// 1. Builds the test binary from the contents of /test/e2e-node
// 2. Retrieves the MachineConfig and ControllerConfigs from the Kube API
// server.
// 3. Creates a temporary directory on the target node.
// 4. Copies the built binary, the MachineConfig JSON, and the ControllerConfig
// JSON to the temporary directory on the node.
// 5. Executes the test binary on the node and collects the results in the
// desired format.
func (n NodeE2ETestOpts) run(t *testing.T, cs *framework.ClientSet) error {
	// First, validate that the options given are actually valid.
	if err := n.validate(); err != nil {
		return err
	}

	// Get the current MachineConfig from the given node.
	currentMC := n.Node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]

	// If the node doesn't have this annotation, something is seriously wrong so stop here.
	if currentMC == "" {
		return fmt.Errorf("%s annotation empty for node %s", daemonconsts.CurrentMachineConfigAnnotationKey, n.Node.Name)
	}

	// Validates the given test binaries to see if one is suitable for our node.
	// If one cannot be found, one will be built automatically. However,
	// post-test, it will be deleted as it will be built in a temp directory
	// managed by the testing.T object which will be automatically cleaned up.
	testBinary, err := n.getOrBuildTestBinaryForNode(t)
	if err != nil {
		return err
	}

	// Writes the MachineConfig and Controllerconfig files to a local temp
	// directory for later staging.
	payloadFiles, err := n.writePayloadFiles(t.TempDir(), cs)
	if err != nil {
		return err
	}

	// Adds the test binary to the list of payloads.
	payloadFiles = append(payloadFiles, testBinary.path)

	// Instantiates a new NodeExec object so we can copy files to the node and
	// execute commands on it.
	nodeExec := NewNodeExecOrFail(t, cs, &NodeExecOpts{
		Node:    &n.Node,
		Verbose: true,
	})

	// Create a remote temporary directory on the node.
	remoteTmpDir, remoteCleanupFunc, err := nodeExec.CreateRemoteTempDir()
	if err != nil {
		return err
	}

	// Wire up the cleanup function so that the remote directory is torn down at
	// the end of the test.
	t.Cleanup(func() {
		require.NoError(t, remoteCleanupFunc())
	})

	// Copy our payload files to the remote temporary directory.
	if err := nodeExec.CopyFilesToNode(payloadFiles, remoteTmpDir); err != nil {
		return err
	}

	// Execute our test binary command on the node.
	results, err := nodeExec.ExecuteCommand(ExecOpts{
		Command: testBinary.getCLIArgs(remoteTmpDir, n.TestNames),
	})

	if err != nil {
		// If the test fails, log its output.
		t.Logf("NodeE2ETest on %s failed:\n%s", n.Node.Name, results.Combined.String())
		return err
	}

	// Handle the test output accordingly.
	return n.handleOutput(t, results.Combined.Bytes())
}

// Looks up or builds a test binary from the provided options.
func (n NodeE2ETestOpts) getOrBuildTestBinaryForNode(t *testing.T) (*TestBinary, error) {
	if len(n.TestBinaries) != 0 {
		for _, testBin := range n.TestBinaries {
			testBin := testBin
			if testBin.matchesNode(n.Node) {
				t.Logf("Found matching test binary for %s at %q for node %s", testBin.osAndArch(), testBin.path, n.Node.Name)
				return n.validateTestBinary(testBin)
			}
		}
	}

	// We haven't found a matching test binary, so let's build one!
	tbo, err := newTestBinaryOptsForNode(n.Node, t.TempDir())
	if err != nil {
		t.Logf("Could not build a binary for node %s: %s", n.Node.Name, err)
		return nil, err
	}

	t.Logf("Did not find preexisting test binary for node %s (%s), will build one...", n.Node.Name, tbo.osAndArch())
	tb, err := tbo.buildBinary(t)
	if err != nil {
		return nil, err
	}

	return n.validateTestBinary(tb)
}

// Validates that a given test binary.
func (n NodeE2ETestOpts) validateTestBinary(tb *TestBinary) (*TestBinary, error) {
	if err := tb.validate(n.TestNames); err != nil {
		return nil, err
	}

	return tb, nil
}

// Validates that the options have been configured correctly.
func (n NodeE2ETestOpts) validate() error {
	isJunit, err := isGoJunitReportAvailable()
	if err != nil {
		return err
	}

	if n.CollectOutputAsJunit && !isJunit {
		return fmt.Errorf("go-junit-report binary not found and junit output requested")
	}

	if n.CollectOutputAsJunit && n.OutputDir == "" {
		return fmt.Errorf("no junit output dir provided")
	}

	if n.CollectOutput && n.OutputDir == "" {
		return fmt.Errorf("no output dir provided")
	}

	return nil
}

// Writes the MachineConfig and ControllerConfig payloads to the local filesystem.
func (n NodeE2ETestOpts) writePayloadFiles(outputDir string, cs *framework.ClientSet) ([]string, error) {
	localCtrlCfgPath := filepath.Join(outputDir, ctrlcfgName)
	localMachineConfigPath := filepath.Join(outputDir, machineconfigName)

	currentMC := n.Node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]

	return writeKubeObjectsToFiles(map[string]func() (interface{}, error){
		localMachineConfigPath: func() (interface{}, error) {
			return cs.MachineconfigurationV1Interface.MachineConfigs().Get(context.TODO(), currentMC, metav1.GetOptions{})
		},
		localCtrlCfgPath: func() (interface{}, error) {
			return cs.MachineconfigurationV1Interface.ControllerConfigs().Get(context.TODO(), ctrlcommon.ControllerConfigName, metav1.GetOptions{})
		},
	})
}

// Writes the test output to disk as junit.
func (n NodeE2ETestOpts) writeAsJunit(t *testing.T, output []byte) error {
	filename := filepath.Join(n.OutputDir, fmt.Sprintf("junit-%s-test-output.xml", n.Node.Name))

	outputFile, err := os.Create(filename)
	if err != nil {
		return err
	}

	defer outputFile.Close()

	cmd := exec.Command("go-junit-report")
	cmd.Stdin = bytes.NewBuffer(output)
	cmd.Stdout = outputFile
	return cmd.Run()
}

// Writes the test output to disk as the standard Golang test output.
func (n NodeE2ETestOpts) writeAsTestLog(t *testing.T, output []byte) error {
	filename := filepath.Join(n.OutputDir, fmt.Sprintf("%s-test-output.log", n.Node.Name))
	t.Logf("Writing test log output to %q", filename)
	return os.WriteFile(filename, []byte(output), 0o755)
}

// Handles the formatting and output of the final test results.
func (n NodeE2ETestOpts) handleOutput(t *testing.T, output []byte) error {
	if !n.CollectOutputAsJunit && !n.CollectOutput {
		t.Logf("Output collection not requested, writing to main test log output:")
		t.Logf("%s", string(output))
		return nil
	}

	if n.CollectOutputAsJunit {
		if err := n.writeAsJunit(t, output); err != nil {
			return nil
		}
	}

	if n.CollectOutput {
		if err := n.writeAsTestLog(t, output); err != nil {
			return err
		}
	}

	return nil
}

// Writes kube objects to local files as JSON.
func writeKubeObjectsToFiles(writeFuncs map[string]func() (interface{}, error)) ([]string, error) {
	files := []string{}

	for filename, writeFunc := range writeFuncs {
		if err := writeKubeObjectToFile(filename, writeFunc); err != nil {
			return nil, err
		}

		files = append(files, filename)
	}

	return files, nil
}

// Writes a given Kube object returned by the query function to a local JSON file.
func writeKubeObjectToFile(filename string, queryFunc func() (interface{}, error)) error {
	obj, err := queryFunc()
	if err != nil {
		return err
	}

	return writeObjectToFileAsJSON(obj, filename)
}

// Writes a given object to a local JSON file.
func writeObjectToFileAsJSON(obj interface{}, filename string) error {
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	return os.WriteFile(filename, jsonBytes, 0o755)
}

// Determines if go-junit-report is available by looking in the PATH for it.
func isGoJunitReportAvailable() (bool, error) {
	_, err := exec.LookPath("go-junit-report")
	if err == nil {
		return true, nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}

	return false, err
}
