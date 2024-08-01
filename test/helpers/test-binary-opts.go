package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Represents test binary build / validation options.
type testBinaryOpts struct {
	// The directory to put the built binary into.
	outputDir string
	// The OS to build the binary for.
	os string
	// The architecture to build the binary for.
	arch string
	// What test names to run. Needed for validation only.
	testNames []string
}

// Constructs a testBinaryOpts instance and ensures that it is valid.
func newTestBinaryOpts(os, arch, outputDir string) (*testBinaryOpts, error) {
	tbo := &testBinaryOpts{
		os:        os,
		arch:      arch,
		outputDir: outputDir,
	}

	if err := tbo.validate(); err != nil {
		return nil, err
	}

	return tbo, nil
}

// Constructs a testBinaryOpts instance from a given node and ensures that it is valid.
func newTestBinaryOptsForNode(node corev1.Node, outputDir string) (*testBinaryOpts, error) {
	return newTestBinaryOpts(node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.Architecture, outputDir)
}

// Ensures that the options are all set correctly.
func (tbo *testBinaryOpts) validate() error {
	if tbo.outputDir == "" {
		return fmt.Errorf("output dir cannot be empty")
	}

	if tbo.os == "" && tbo.arch == "" {
		return fmt.Errorf("must provide os and arch")
	}

	if (tbo.os == "" && tbo.arch != "") || (tbo.os != "" && tbo.arch == "") {
		return fmt.Errorf("both os and arch must be provided")
	}

	if strings.Contains(tbo.outputDir, tbo.os) {
		return fmt.Errorf("output dir cannot have os in its name")
	}

	if strings.Contains(tbo.outputDir, tbo.arch) {
		return fmt.Errorf("output dir cannot have arch in its name")
	}

	_, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("missing required go toolchain")
	}

	return nil
}

// Collects the options into a testBinary object.
func (tbo testBinaryOpts) toTestBinary() *TestBinary {
	return &TestBinary{
		os:   tbo.os,
		arch: tbo.arch,
		path: tbo.getBinaryPath(),
	}
}

// Builds a Golang test binary for the given OS and arhictecture and places it
// into the appropriate output directory, creating all subdirectories.
func (tbo testBinaryOpts) buildBinary(t *testing.T) (*TestBinary, error) {
	if err := tbo.validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(tbo.getBinaryDirPath(), 0o755); err != nil {
		return nil, err
	}

	cmd := exec.Command("go", "test", "-c", "-o", tbo.getBinaryPath(), ".")
	t.Logf("Building test binary for %s, using %q", tbo.osAndArch(), cmd)
	cmd.Dir = filepath.Join(repoRoot, "test/e2e-node")
	cmd.Env = append(os.Environ(), []string{
		fmt.Sprintf("GOOS=%s", tbo.os),
		fmt.Sprintf("GOARCH=%s", tbo.arch),
		"CGO_ENABLED=0",
	}...)

	cmdOut, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not build e2e-node binary for %s, %s: %w", tbo.osAndArch(), string(cmdOut), err)
	}

	return tbo.toTestBinary(), nil
}

// Concatenates the given OS and arch into a single string, e.g., linux/amd64.
func (tbo testBinaryOpts) osAndArch() string {
	return fmt.Sprintf("%s/%s", tbo.os, tbo.arch)
}

// Computes the directory where the binary will be placed.
func (tbo testBinaryOpts) getBinaryDirPath() string {
	return filepath.Join(tbo.outputDir, tbo.osAndArch())
}

// Computes the full absolute path to the local binary.
func (tbo testBinaryOpts) getBinaryPath() string {
	return filepath.Join(tbo.getBinaryDirPath(), "e2e-node")
}
