package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

const (
	sshdPath   = "/etc/ssh/sshd_config"
	renderedMC = "rendered-worker-abc"
)

func TestRunFileManaged(t *testing.T) {
	t.Parallel()

	base := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-00\n")
	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n")
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC, "99-worker-ssh", "00-worker")

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, base, overlay, rendered), inspectArgs{path: sshdPath, pool: "worker", output: "text"}, &buf)
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "Pool:             worker")
	assert.Contains(t, out, "Configuration:    current")
	assert.Contains(t, out, "Source:           MCP status.configuration")
	assert.Contains(t, out, "Rendered MC:      rendered-worker-abc")
	assert.Contains(t, out, "File:             /etc/ssh/sshd_config")
	assert.Contains(t, out, "  00-worker")
	assert.Contains(t, out, "  99-worker-ssh")
	assert.Contains(t, out, "Last writer:")
	assert.NotContains(t, out, "PermitRootLogin no")
	assert.Contains(t, out, "omitted (pass --show-content to print)")
}

func TestRunFileShowContent(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{path: sshdPath, pool: "worker", output: "text", showContent: true}, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "PermitRootLogin no")
}

func TestRunFileMissing(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "present\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{path: "/etc/example", pool: "worker", output: "text", showContent: true}, &buf)
	require.NoError(t, err, "absent managed file is a successful inspection")
	out := buf.String()
	assert.Contains(t, out, "This path is not managed by the rendered MachineConfig.")
	assert.NotContains(t, out, "Expected content:")
	assert.NotContains(t, out, "present")
}

func TestRunFileEmpty(t *testing.T) {
	t.Parallel()

	rendered := mcWithFileBytes(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, "/etc/empty", nil)
	pool := mcpWithSources(t, "worker", renderedMC)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{path: "/etc/empty", pool: "worker", output: "text", showContent: true}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Exists:           yes")
	assert.Contains(t, out, "Expected size:    0 bytes")
	assert.Contains(t, out, "<empty>")
}

func TestRunFileAttributionUnavailable(t *testing.T) {
	t.Parallel()

	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n")
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "canonical-from-render\n")
	pool := mcpWithSources(t, "worker", renderedMC, "00-worker", "99-worker-ssh")

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, overlay, rendered), inspectArgs{path: sshdPath, pool: "worker", output: "text"}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Rendered MC:      rendered-worker-abc")
	assert.Contains(t, out, "Expected size:")
	assert.Contains(t, out, "Attribution:      unavailable")
	assert.Contains(t, out, "00-worker")
}

func TestRunFileJSON(t *testing.T) {
	t.Parallel()

	base := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-00\n")
	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n")
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "secret\n")
	pool := mcpWithSources(t, "worker", renderedMC, "99-worker-ssh", "00-worker")

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, base, overlay, rendered), inspectArgs{path: sshdPath, pool: "worker", output: "json"}, &buf)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "worker", got["pool"])
	assert.Equal(t, "current", got["configuration"])
	assert.Equal(t, "MCP status.configuration", got["configurationSource"])
	assert.Equal(t, renderedMC, got["renderedMachineConfig"])
	assert.Equal(t, sshdPath, got["path"])
	assert.Equal(t, true, got["found"])
	assert.Equal(t, []any{"00-worker", "99-worker-ssh"}, got["writers"])
	assert.Equal(t, "99-worker-ssh", got["lastWriter"])
	assert.Equal(t, true, got["attributionAvailable"])
	_, hasContent := got["expectedContent"]
	assert.False(t, hasContent)
}

func TestRunFileTargetOrigin(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-spec\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	pool.Status.Configuration = mcfgv1.MachineConfigPoolStatusConfiguration{}

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{path: sshdPath, pool: "worker", output: "text"}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Configuration:    target")
	assert.Contains(t, out, "Source:           MCP spec.configuration")
}

func TestRunFilePoolMissingIsError(t *testing.T) {
	t.Parallel()

	err := runFile(context.Background(), newFakeGetter(t), inspectArgs{path: sshdPath, pool: "worker", output: "text"}, &bytes.Buffer{})
	require.Error(t, err)
	assert.ErrorIs(t, err, cluster.ErrPoolNotFound)
}

func TestFileCommandRequiresPool(t *testing.T) {
	t.Parallel()

	cmd := newFileCommand()
	cmd.SetArgs([]string{sshdPath})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required flag")
}

func TestFileCommandHasKubeconfigFlags(t *testing.T) {
	t.Parallel()

	cmd := newFileCommand()
	assert.NotNil(t, cmd.Flags().Lookup("kubeconfig"))
	assert.NotNil(t, cmd.Flags().Lookup("context"))
	assert.NotNil(t, cmd.Flags().Lookup("from-file"))
	assert.NotNil(t, cmd.Flags().Lookup("node"))
	assert.NotNil(t, cmd.Flags().Lookup("must-gather"))
}

func TestRunFileFromFileMatch(t *testing.T) {
	t.Parallel()

	contents := "PermitRootLogin no\n"
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, contents)
	pool := mcpWithSources(t, "worker", renderedMC)
	local := writeTempFile(t, "sshd_config", contents)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:     sshdPath,
		pool:     "worker",
		output:   "text",
		fromFile: local,
	}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Comparison:       MATCH")
	assert.Contains(t, out, "From file:        "+local)
	assert.NotContains(t, out, "CONTENT MISMATCH")
	assert.NotContains(t, out, "Unified diff:")
	assert.NotContains(t, out, "PermitRootLogin no")
}

func TestRunFileFromFileMismatch(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	local := writeTempFile(t, "sshd_config", "PermitRootLogin yes\n")

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:     sshdPath,
		pool:     "worker",
		output:   "text",
		fromFile: local,
	}, &buf)
	require.NoError(t, err, "content mismatch is a successful inspection")
	out := buf.String()
	assert.Contains(t, out, "Comparison:       CONTENT MISMATCH")
	assert.Contains(t, out, "expected 19 bytes, got 20 bytes")
	assert.Contains(t, out, "Unified diff:")
	assert.Contains(t, out, "-PermitRootLogin no")
	assert.Contains(t, out, "+PermitRootLogin yes")
}

func TestRunFileFromFileJSON(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	local := writeTempFile(t, "sshd_config", "PermitRootLogin yes\n")

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:     sshdPath,
		pool:     "worker",
		output:   "json",
		fromFile: local,
	}, &buf)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, local, got["fromFile"])
	assert.Equal(t, false, got["match"])
	assert.Equal(t, float64(20), got["actualSize"])
	assert.Equal(t, float64(19), got["expectedSize"])
	diffStr, ok := got["diff"].(string)
	require.True(t, ok)
	assert.Contains(t, diffStr, "-PermitRootLogin no")
	assert.Contains(t, diffStr, "+PermitRootLogin yes")
	_, hasContent := got["expectedContent"]
	assert.False(t, hasContent)
}

func TestRunFileFromFileUnmanaged(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "present\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	local := writeTempFile(t, "example", "local-only\n")

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:     "/etc/example",
		pool:     "worker",
		output:   "text",
		fromFile: local,
	}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "This path is not managed by the rendered MachineConfig.")
	assert.Contains(t, out, "Local file:       "+local+" (11 bytes)")
	assert.Contains(t, out, "No content comparison was performed")
	assert.NotContains(t, out, "Unified diff:")
	assert.NotContains(t, out, "local-only")
}

func TestRunFileFromFileMissingLocal(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "present\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:     sshdPath,
		pool:     "worker",
		output:   "text",
		fromFile: missing,
	}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read --from-file")
	assert.Contains(t, err.Error(), missing)
}

func TestFileCommandNodeAndFromFileMutuallyExclusive(t *testing.T) {
	t.Parallel()

	cmd := newFileCommand()
	cmd.SetArgs([]string{sshdPath, "--pool", "worker", "--node", "worker-0", "--from-file", "./sshd_config"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Equal(t, "cannot use --from-file and --node together", err.Error())
}

func TestRunFileNodeAndFromFileMutuallyExclusive(t *testing.T) {
	t.Parallel()

	err := runFile(context.Background(), newFakeGetter(t), inspectArgs{
		path:     sshdPath,
		pool:     "worker",
		fromFile: "./sshd_config",
		node:     "worker-0",
	}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Equal(t, "cannot use --from-file and --node together", err.Error())
}

func TestRunFileNodeMatch(t *testing.T) {
	t.Parallel()

	contents := "PermitRootLogin no\n"
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, contents)
	pool := mcpWithSources(t, "worker", renderedMC)
	nr := &fakeNodeReader{content: []byte(contents)}

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		output:     "text",
		node:       "worker-0",
		nodeReader: nr,
	}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "Comparison:       MATCH")
	assert.NotContains(t, out, "CONTENT MISMATCH")
	assert.NotContains(t, out, "Unified diff:")
	assert.Equal(t, "worker-0", nr.node)
	assert.Equal(t, sshdPath, nr.path)
}

func TestRunFileNodeMismatch(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	nr := &fakeNodeReader{content: []byte("PermitRootLogin yes\n")}

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		output:     "text",
		node:       "worker-0",
		nodeReader: nr,
	}, &buf)
	require.NoError(t, err, "content mismatch is a successful inspection")
	out := buf.String()
	assert.Contains(t, out, "Comparison:       CONTENT MISMATCH")
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "expected 19 bytes, got 20 bytes")
	assert.Contains(t, out, "-PermitRootLogin no")
	assert.Contains(t, out, "+PermitRootLogin yes")
}

func TestRunFileNodeModeMismatch(t *testing.T) {
	t.Parallel()

	contents := "PermitRootLogin no\n"
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, contents)
	pool := mcpWithSources(t, "worker", renderedMC)
	mode := 0o755
	nr := &fakeNodeReader{content: []byte(contents), mode: &mode}

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		output:     "text",
		node:       "worker-0",
		nodeReader: nr,
	}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Comparison:       MODE MISMATCH")
	assert.Contains(t, out, "Mode:             expected 0644, actual 0755")
	assert.NotContains(t, out, "Unified diff:")
}

func TestRunFileNodeMissing(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		output:     "text",
		node:       "worker-0",
		nodeReader: &fakeNodeReader{err: node.ErrFileNotFound},
	}, &buf)
	require.NoError(t, err, "missing node file is a successful inspection")
	out := buf.String()
	assert.Contains(t, out, "File exists in rendered MC, but is MISSING ON NODE worker-0.")
	assert.NotContains(t, out, "Unified diff:")
}

func TestRunFileNodeUnmanagedExists(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "present\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       "/etc/example",
		pool:       "worker",
		output:     "text",
		node:       "worker-0",
		nodeReader: &fakeNodeReader{content: []byte("local-only\n")},
	}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "This path is not managed by the rendered MachineConfig.")
	assert.Contains(t, out, "Node file:        exists (11 bytes)")
	assert.NotContains(t, out, "Unified diff:")
	assert.NotContains(t, out, "local-only")
}

func TestRunFileNodeReadError(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "present\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		output:     "text",
		node:       "worker-0",
		nodeReader: &fakeNodeReader{err: node.ErrMCDUnavailable},
	}, &bytes.Buffer{})
	require.Error(t, err)
	assert.ErrorIs(t, err, node.ErrMCDUnavailable)
	assert.Contains(t, err.Error(), `failed to read "/etc/ssh/sshd_config" from node "worker-0"`)
}

func TestRunFileNodeJSON(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	var buf bytes.Buffer
	err := runFile(context.Background(), newFakeGetter(t, pool, rendered), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		output:     "json",
		node:       "worker-0",
		nodeReader: &fakeNodeReader{content: []byte("PermitRootLogin yes\n")},
	}, &buf)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "worker-0", got["node"])
	assert.Equal(t, false, got["match"])
	assert.Equal(t, true, got["nodeFileFound"])
	assert.Equal(t, float64(20), got["actualSize"])
	diffStr, ok := got["diff"].(string)
	require.True(t, ok)
	assert.Contains(t, diffStr, "-PermitRootLogin no")
}

func TestFileCommandMustGatherAndFromFileMutuallyExclusive(t *testing.T) {
	t.Parallel()

	cmd := newFileCommand()
	cmd.SetArgs([]string{sshdPath, "--pool", "worker", "--must-gather", "./mg", "--from-file", "./sshd_config"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Equal(t, "cannot use --must-gather and --from-file together", err.Error())
}

func TestRunFileMustGatherAndFromFileMutuallyExclusive(t *testing.T) {
	t.Parallel()

	err := runFile(context.Background(), newFakeGetter(t), inspectArgs{
		path:       sshdPath,
		pool:       "worker",
		fromFile:   "./sshd_config",
		mustGather: "./mg",
	}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Equal(t, "cannot use --must-gather and --from-file together", err.Error())
}

func TestFileCommandHelpContainsExamples(t *testing.T) {
	t.Parallel()

	cmd := newFileCommand()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "mcdiff file /etc/chrony.conf --pool worker --node worker-0")
	assert.Contains(t, out, "--from-file ./sshd_config")
	assert.Contains(t, out, "--must-gather ./must-gather.local")
}

func TestRunFileMustGatherOffline(t *testing.T) {
	t.Parallel()

	dir := writeMustGatherFixture(t, "canonical-from-render\n", nil)
	var buf bytes.Buffer
	o := &fileOptions{pool: "worker", mustGather: dir, output: "text", out: &buf}
	err := o.run(context.Background(), sshdPath)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Archive:          Must-Gather Archive ("+dir+")")
	assert.Contains(t, out, "Pool:             worker")
	assert.Contains(t, out, "Rendered MC:      rendered-worker-abc")
	assert.Contains(t, out, "  99-worker-ssh")
	assert.Contains(t, out, "Last writer:")
}

func TestRunFileMustGatherNodeDiff(t *testing.T) {
	t.Parallel()

	dir := writeMustGatherFixture(t, "PermitRootLogin no\n", map[string]string{"worker-0": "PermitRootLogin yes\n"})
	var buf bytes.Buffer
	o := &fileOptions{pool: "worker", mustGather: dir, node: "worker-0", output: "text", out: &buf}
	err := o.run(context.Background(), sshdPath)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Archive:          Must-Gather Archive ("+dir+")")
	assert.Contains(t, out, "Comparison:       CONTENT MISMATCH")
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "-PermitRootLogin no")
	assert.Contains(t, out, "+PermitRootLogin yes")
}

func TestRunFileMustGatherJSON(t *testing.T) {
	t.Parallel()

	dir := writeMustGatherFixture(t, "PermitRootLogin no\n", nil)
	var buf bytes.Buffer
	o := &fileOptions{pool: "worker", mustGather: dir, output: "json", out: &buf}
	err := o.run(context.Background(), sshdPath)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, dir, got["mustGatherDir"])
	assert.Equal(t, "worker", got["pool"])
	assert.Equal(t, "99-worker-ssh", got["lastWriter"])
}

func TestRunFileMustGatherMissingPool(t *testing.T) {
	t.Parallel()

	dir := writeMustGatherFixture(t, "x\n", nil)
	o := &fileOptions{pool: "infra", mustGather: dir, output: "text", out: &bytes.Buffer{}}
	err := o.run(context.Background(), sshdPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, cluster.ErrPoolNotFound)
}

func TestRunFileMustGatherUnmanagedPath(t *testing.T) {
	t.Parallel()

	dir := writeMustGatherFixture(t, "present\n", nil)
	var buf bytes.Buffer
	o := &fileOptions{pool: "worker", mustGather: dir, output: "text", out: &buf}
	err := o.run(context.Background(), "/etc/example")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "This path is not managed by the rendered MachineConfig.")
}

func writeMustGatherFixture(t *testing.T, renderedContents string, nodeFiles map[string]string) string {
	t.Helper()
	root := t.TempDir()
	mcDir := filepath.Join(root, "cluster-scoped-resources", "machineconfiguration.openshift.io", "machineconfigs")
	poolDir := filepath.Join(root, "cluster-scoped-resources", "machineconfiguration.openshift.io", "machineconfigpools")
	nodeDir := filepath.Join(root, "cluster-scoped-resources", "core", "nodes")
	require.NoError(t, os.MkdirAll(mcDir, 0o755))
	require.NoError(t, os.MkdirAll(poolDir, 0o755))
	require.NoError(t, os.MkdirAll(nodeDir, 0o755))

	writeGatherYAML(t, filepath.Join(mcDir, "00-worker.yaml"), mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-00\n"))
	writeGatherYAML(t, filepath.Join(mcDir, "99-worker-ssh.yaml"), mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n"))
	writeGatherYAML(t, filepath.Join(mcDir, renderedMC+".yaml"), mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, renderedContents))
	writeGatherYAML(t, filepath.Join(poolDir, "worker.yaml"), mcpWithSources(t, "worker", renderedMC, "99-worker-ssh", "00-worker"))

	for name, contents := range nodeFiles {
		writeGatherYAML(t, filepath.Join(nodeDir, name+".yaml"), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
		host := filepath.Join(root, "nodes", name, "host", "etc", "ssh")
		require.NoError(t, os.MkdirAll(host, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(host, "sshd_config"), []byte(contents), 0o644))
	}
	return root
}

func writeGatherYAML(t *testing.T, path string, obj any) {
	t.Helper()
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

type fakeNodeReader struct {
	content []byte
	mode    *int
	err     error
	node    string
	path    string
}

func (f *fakeNodeReader) ReadFile(_ context.Context, nodeName, path string) ([]byte, *int, error) {
	f.node = nodeName
	f.path = path
	return f.content, f.mode, f.err
}

func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(contents), 0o600))
	return p
}

func newFakeGetter(t *testing.T, objs ...runtime.Object) cluster.Getter {
	t.Helper()
	return cluster.NewKubeGetter(fake.NewSimpleClientset(objs...))
}

func mcpWithSources(t *testing.T, poolName, renderedName string, sourceNames ...string) *mcfgv1.MachineConfigPool {
	t.Helper()
	refs := make([]corev1.ObjectReference, 0, len(sourceNames))
	for _, name := range sourceNames {
		refs = append(refs, corev1.ObjectReference{Kind: "MachineConfig", Name: name})
	}
	cfg := mcfgv1.MachineConfigPoolStatusConfiguration{
		ObjectReference: corev1.ObjectReference{Name: renderedName},
		Source:          refs,
	}
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec:       mcfgv1.MachineConfigPoolSpec{Configuration: cfg},
		Status:     mcfgv1.MachineConfigPoolStatus{Configuration: cfg},
	}
}

func mcWithFile(t *testing.T, name, role, path, contents string) *mcfgv1.MachineConfig {
	t.Helper()
	return mcWithFileBytes(t, name, role, path, []byte(contents))
}

func mcWithFileBytes(t *testing.T, name, role, path string, contents []byte) *mcfgv1.MachineConfig {
	t.Helper()
	if contents == nil {
		contents = []byte{}
	}
	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage: ign3types.Storage{
			Files: []ign3types.File{ctrlcommon.NewIgnFileBytes(path, contents)},
		},
	}
	raw, err := json.Marshal(ign)
	require.NoError(t, err)
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				ctrlcommon.MachineConfigRoleLabel: role,
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{Raw: raw},
		},
	}
}
