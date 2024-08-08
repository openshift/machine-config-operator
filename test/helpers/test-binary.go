package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Represents a compiled test binary.
type TestBinary struct {
	os   string
	arch string
	path string
}

// Allows one to bring their own prebuilt test binary if they wish.
func NewTestBinary(os, arch, path string) (*TestBinary, error) {
	tb := &TestBinary{
		os:   os,
		arch: arch,
		path: path,
	}

	if err := tb.validate(nil); err != nil {
		return nil, err
	}

	return tb, nil
}

// Determines if a given node matches the test binary's OS and architecture.
func (tb *TestBinary) matchesNode(node corev1.Node) bool {
	return tb.os == node.Status.NodeInfo.OperatingSystem && tb.arch == node.Status.NodeInfo.Architecture
}

// Gets a list of tests from the test binary.
func (tb *TestBinary) getTestList() ([]string, error) {
	cmd := exec.Command(tb.path, "-test.list=.")

	cmdOut, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not get list of tests, %s: %w", string(cmdOut), err)
	}

	names := []string{}
	for _, line := range strings.Split(string(cmdOut), "\n") {
		if strings.HasPrefix(line, "Test") {
			names = append(names, strings.TrimSpace(line))
		}
	}

	return names, nil
}

// Validates that the binary exists within the path provided as well as whether
// the binary provides all of the given test names (if any).
func (tb *TestBinary) validate(testNames []string) error {
	if _, err := os.Stat(tb.path); err != nil {
		return err
	}

	if testNames == nil || len(testNames) == 0 {
		return nil
	}

	for _, name := range testNames {
		if !strings.HasPrefix(name, "Test") {
			return fmt.Errorf("all tests must be prefixed with the word 'Test', found invalid test name: %s", name)
		}
	}

	testList, err := tb.getTestList()
	if err != nil {
		return err
	}

	for _, name := range testNames {
		if !ctrlcommon.InSlice(name, testList) {
			return fmt.Errorf("unknown test %q, known: %v", name, testList)
		}
	}

	return nil
}

// Builds a collection of CLI args including the various test names to run.
func (tb *TestBinary) getCLIArgs(remoteTmpDir string, testNames []string) []string {
	args := []string{
		filepath.Join(remoteTmpDir, filepath.Base(tb.path)),
		"-test.v",
		"-machineconfig",
		filepath.Join(remoteTmpDir, machineconfigName),
		"-ctrlconfig",
		filepath.Join(remoteTmpDir, ctrlcfgName),
	}

	if len(testNames) == 0 {
		return args
	}

	// Concatenates each of the testnames into the following: (TestName1|TestName2)
	// Only needed when one only wants to run a specific test. Note: Does not handle regex escaping.
	testNameArgs := fmt.Sprintf("(%s)", strings.Join(testNames, "|"))
	return append(args, fmt.Sprintf("-test.run=%s", testNameArgs))
}

// Gets the OS and architecture as a single concatenated string.
func (tb *TestBinary) osAndArch() string {
	tbo := &testBinaryOpts{
		os:   tb.os,
		arch: tb.arch,
	}
	return tbo.osAndArch()
}

// Given a list of nodes and an output directory, will return a list of
// testBinaryOpts. The returned list will only contain unique operating system
// / architecture combinations. For example, if a list of 10 nodes is given and
// all nodes are linux/amd64, only a single TestBinary instance will be
// returned. If 5 are linux/arm64 and 5 are linux/amd64, two TestBinary
// instances will be returned.
func getTestBinaryOptsForNodes(outputDir string, nodeList *corev1.NodeList) ([]*testBinaryOpts, error) {
	tbos := []*testBinaryOpts{}

	processedOSAndArches := sets.Set[string]{}

	for _, node := range nodeList.Items {
		tbo, err := newTestBinaryOptsForNode(node, outputDir)
		if err != nil {
			return nil, err
		}

		if !processedOSAndArches.Has(tbo.osAndArch()) {
			processedOSAndArches.Insert(tbo.osAndArch())
			tbos = append(tbos, tbo)
		}
	}

	return tbos, nil
}
