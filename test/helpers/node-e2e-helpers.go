package helpers

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	_, b, _, _ = runtime.Caller(0)

	repoRoot = filepath.Join(filepath.Dir(b), "../..")
)

// Computes the root directory of the MCO repository. Useful for constructing
// absolute paths to specific locations within the MCO repo for test and other
// automation purposes.
func GetRepoRoot() string {
	return repoRoot
}

// Returns the ARTIFACT_DIR if found, otherwise returns the current working
// directory.
func ArtifactDirOrLocalDir(t *testing.T) (string, error) {
	outputDir, ok := os.LookupEnv("ARTIFACT_DIR")
	if ok && outputDir != "" {
		t.Logf("ARTIFACT_DIR found, using...")
		return outputDir, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	t.Logf("ARTIFACT_DIR not found, using current directory (%s)", cwd)

	return cwd, nil
}

// Ensures that a given function only runs once; even if it is called multiple
// times. Note: This will return the same error value regardless of how many
// times the function is run.
func MakeIdempotentWithError(f func() error) func() error {
	hasRun := false
	var err error

	return func() error {
		if !hasRun {
			err = f()
			hasRun = true
		}

		return err
	}
}

// Builds an E2E binary for the given node and stores it in a testing.T-managed temp dir.
func BuildE2EBinaryForNode(t *testing.T, node corev1.Node) (*TestBinary, error) {
	tbo, err := newTestBinaryOptsForNode(node, t.TempDir())
	if err != nil {
		return nil, err
	}

	return tbo.buildBinary(t)
}

// Builds an E2E binary for all nodes within a given cluster. Note: This will
// return one testBinary instance per OS / arch combination. This test binary
// may be reused across all nodes matching the same OS and architecture.
func BuildE2EBinariesForNodes(t *testing.T, cs *framework.ClientSet) ([]*TestBinary, error) {
	nodeList, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	tboList, err := getTestBinaryOptsForNodes(t.TempDir(), nodeList)
	if err != nil {
		return nil, err
	}

	testBinaries := []*TestBinary{}

	for _, tbo := range tboList {
		tb, err := tbo.buildBinary(t)
		if err != nil {
			return nil, err
		}

		testBinaries = append(testBinaries, tb)
	}

	return testBinaries, nil
}
