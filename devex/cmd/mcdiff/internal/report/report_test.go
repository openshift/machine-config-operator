package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/attribution"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/diff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWriteManagedFileOmitsContentByDefault(t *testing.T) {
	t.Parallel()

	mode := 0o644
	var buf bytes.Buffer
	err := Write(&buf, managedPoolFile(mode, "PermitRootLogin no\n"), Options{})
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "Pool:             worker")
	assert.Contains(t, out, "Configuration:    current")
	assert.Contains(t, out, "Source:           MCP status.configuration")
	assert.Contains(t, out, "Rendered MC:      rendered-worker-abc")
	assert.Contains(t, out, "File:             /etc/ssh/sshd_config")
	assert.Contains(t, out, "Exists:           yes")
	assert.Contains(t, out, "Mode:             0644")
	assert.Contains(t, out, "  00-worker")
	assert.Contains(t, out, "  99-worker-ssh")
	assert.Contains(t, out, "  99-worker-ssh")
	assert.Contains(t, out, "Last writer:")
	assert.Contains(t, out, "omitted (pass --show-content to print)")
	assert.NotContains(t, out, "PermitRootLogin no")
}

func TestWriteShowContent(t *testing.T) {
	t.Parallel()

	mode := 0o644
	var buf bytes.Buffer
	err := Write(&buf, managedPoolFile(mode, "PermitRootLogin no\n"), Options{ShowContent: true})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "PermitRootLogin no")
}

func TestWriteMissingFile(t *testing.T) {
	t.Parallel()

	pf := managedPoolFile(0o644, "present\n")
	pf.Path = "/etc/example"
	pf.Found = false
	pf.Expected = nil

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, pf, Options{ShowContent: true}))
	out := buf.String()
	assert.Contains(t, out, "Exists:           no")
	assert.Contains(t, out, "This path is not managed by the rendered MachineConfig.")
	assert.NotContains(t, out, "Expected content:")
	assert.NotContains(t, out, "present")
}

func TestWriteEmptyFile(t *testing.T) {
	t.Parallel()

	mode := 0o644
	pf := managedPoolFile(mode, "")
	pf.Expected = []byte{}
	pf.Found = true

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, pf, Options{ShowContent: true}))
	out := buf.String()
	assert.Contains(t, out, "Exists:           yes")
	assert.Contains(t, out, "Expected size:    0 bytes")
	assert.Contains(t, out, "<empty>")
}

func TestWriteAttributionUnavailable(t *testing.T) {
	t.Parallel()

	mode := 0o644
	pf := managedPoolFile(mode, "canonical\n")
	pf.Attribution = nil
	pf.AttributionErr = errors.New("source MachineConfig 99-worker-ssh could not be retrieved")

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, pf, Options{}))
	out := buf.String()
	assert.Contains(t, out, "Expected size:    10 bytes")
	assert.Contains(t, out, "Attribution:      unavailable")
	assert.Contains(t, out, "99-worker-ssh could not be retrieved")
}

func TestWriteJSONOmitsContent(t *testing.T) {
	t.Parallel()

	mode := 0o644
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(mode, "secret\n"), Options{Format: "json"}))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "worker", got["pool"])
	assert.Equal(t, "current", got["configuration"])
	assert.Equal(t, "MCP status.configuration", got["configurationSource"])
	assert.Equal(t, "rendered-worker-abc", got["renderedMachineConfig"])
	assert.Equal(t, "/etc/ssh/sshd_config", got["path"])
	assert.Equal(t, true, got["found"])
	assert.Equal(t, float64(420), got["mode"])
	assert.Equal(t, float64(7), got["expectedSize"])
	assert.Equal(t, []any{"00-worker", "99-worker-ssh"}, got["writers"])
	assert.Equal(t, "99-worker-ssh", got["lastWriter"])
	assert.Equal(t, true, got["attributionAvailable"])
	_, hasContent := got["expectedContent"]
	assert.False(t, hasContent)
}

func TestWriteJSONIncludesContentWhenRequested(t *testing.T) {
	t.Parallel()

	mode := 0o644
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(mode, "secret\n"), Options{Format: "json", ShowContent: true}))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "secret\n", got["expectedContent"])
}

func TestWriteJSONTargetOrigin(t *testing.T) {
	t.Parallel()

	pf := managedPoolFile(0o644, "x\n")
	pf.Origin = cluster.ConfigurationOrigin{Kind: cluster.ConfigurationTarget, Source: "MCP spec.configuration"}

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, pf, Options{Format: "json"}))
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "target", got["configuration"])
	assert.Equal(t, "MCP spec.configuration", got["configurationSource"])
}

func TestWriteFromFileMatch(t *testing.T) {
	t.Parallel()

	contents := "PermitRootLogin no\n"
	cmp := diff.Compare([]byte(contents), []byte(contents), "/etc/ssh/sshd_config", "./sshd_config")
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, contents), Options{
		FromFile: "./sshd_config",
		Actual:   []byte(contents),
		Diff:     &cmp,
	}))
	out := buf.String()
	assert.Contains(t, out, "Comparison:       MATCH")
	assert.Contains(t, out, "From file:        ./sshd_config")
	assert.NotContains(t, out, "Unified diff:")
	assert.NotContains(t, out, "omitted (pass --show-content to print)")
}

func TestWriteFromFileMismatch(t *testing.T) {
	t.Parallel()

	expected := "PermitRootLogin no\n"
	actual := "PermitRootLogin yes\n"
	cmp := diff.Compare([]byte(expected), []byte(actual), "/etc/ssh/sshd_config", "./sshd_config")
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, expected), Options{
		FromFile: "./sshd_config",
		Actual:   []byte(actual),
		Diff:     &cmp,
	}))
	out := buf.String()
	assert.Contains(t, out, "Comparison:       CONTENT MISMATCH")
	assert.Contains(t, out, "expected 19 bytes, got 20 bytes")
	assert.Contains(t, out, "Unified diff:")
	assert.Contains(t, out, "-PermitRootLogin no")
	assert.Contains(t, out, "+PermitRootLogin yes")
}

func TestWriteFromFileUnmanaged(t *testing.T) {
	t.Parallel()

	pf := managedPoolFile(0o644, "present\n")
	pf.Path = "/etc/example"
	pf.Found = false
	pf.Expected = nil

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, pf, Options{
		FromFile: "./example",
		Actual:   []byte("local-only\n"),
	}))
	out := buf.String()
	assert.Contains(t, out, "This path is not managed by the rendered MachineConfig.")
	assert.Contains(t, out, "Local file:       ./example (11 bytes)")
	assert.Contains(t, out, "No content comparison was performed")
	assert.NotContains(t, out, "Unified diff:")
	assert.NotContains(t, out, "local-only")
}

func TestWriteFromFileJSON(t *testing.T) {
	t.Parallel()

	expected := "PermitRootLogin no\n"
	actual := "PermitRootLogin yes\n"
	cmp := diff.Compare([]byte(expected), []byte(actual), "/etc/ssh/sshd_config", "./sshd_config")
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, expected), Options{
		Format:   "json",
		FromFile: "./sshd_config",
		Actual:   []byte(actual),
		Diff:     &cmp,
	}))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "./sshd_config", got["fromFile"])
	assert.Equal(t, false, got["match"])
	assert.Equal(t, float64(20), got["actualSize"])
	diffStr, ok := got["diff"].(string)
	require.True(t, ok)
	assert.Contains(t, diffStr, "-PermitRootLogin no")
	_, hasContent := got["expectedContent"]
	assert.False(t, hasContent)
}

func TestWriteNodeMatch(t *testing.T) {
	t.Parallel()

	contents := "PermitRootLogin no\n"
	cmp := diff.Compare([]byte(contents), []byte(contents), "/etc/ssh/sshd_config", "node:worker-0")
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, contents), Options{
		Node:   "worker-0",
		Actual: []byte(contents),
		Diff:   &cmp,
	}))
	out := buf.String()
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "Comparison:       MATCH")
	assert.NotContains(t, out, "Unified diff:")
}

func TestWriteMustGatherArchive(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, "x\n"), Options{MustGather: "./must-gather-archive"}))
	assert.Contains(t, buf.String(), "Archive:          Must-Gather Archive (./must-gather-archive)")
	assert.Contains(t, buf.String(), "Source:           MCP status.configuration")
}

func TestWriteMustGatherJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, "x\n"), Options{Format: "json", MustGather: "./mg"}))
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "./mg", got["mustGatherDir"])
}

func TestWriteNodeMismatch(t *testing.T) {
	t.Parallel()

	expected := "PermitRootLogin no\n"
	actual := "PermitRootLogin yes\n"
	cmp := diff.Compare([]byte(expected), []byte(actual), "/etc/ssh/sshd_config", "node:worker-0")
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, expected), Options{
		Node:   "worker-0",
		Actual: []byte(actual),
		Diff:   &cmp,
	}))
	out := buf.String()
	assert.Contains(t, out, "Comparison:       CONTENT MISMATCH")
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "-PermitRootLogin no")
}

func TestWriteNodeModeMismatch(t *testing.T) {
	t.Parallel()

	contents := "PermitRootLogin no\n"
	expectedMode := 0o644
	actualMode := 0o755
	cmp := diff.WithModes(diff.Compare([]byte(contents), []byte(contents), "/etc/ssh/sshd_config", "node:worker-0"), &expectedMode, &actualMode)
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, contents), Options{
		Node:   "worker-0",
		Actual: []byte(contents),
		Diff:   &cmp,
	}))
	out := buf.String()
	assert.Contains(t, out, "Comparison:       MODE MISMATCH")
	assert.Contains(t, out, "Mode:             expected 0644, actual 0755")
	assert.NotContains(t, out, "Unified diff:")
}

func TestWriteNodeMissing(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, "present\n"), Options{
		Node:          "worker-0",
		ActualMissing: true,
	}))
	out := buf.String()
	assert.Contains(t, out, "File exists in rendered MC, but is MISSING ON NODE worker-0.")
	assert.NotContains(t, out, "Unified diff:")
}

func TestWriteNodeJSON(t *testing.T) {
	t.Parallel()

	expected := "PermitRootLogin no\n"
	actual := "PermitRootLogin yes\n"
	cmp := diff.Compare([]byte(expected), []byte(actual), "/etc/ssh/sshd_config", "node:worker-0")
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, managedPoolFile(0o644, expected), Options{
		Format: "json",
		Node:   "worker-0",
		Actual: []byte(actual),
		Diff:   &cmp,
	}))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "worker-0", got["node"])
	assert.Equal(t, false, got["match"])
	assert.Equal(t, true, got["nodeFileFound"])
	assert.Equal(t, float64(20), got["actualSize"])
	diffStr, ok := got["diff"].(string)
	require.True(t, ok)
	assert.Contains(t, diffStr, "+PermitRootLogin yes")
}

func managedPoolFile(mode int, contents string) *cluster.PoolFile {
	return &cluster.PoolFile{
		Pool:     &mcfgv1.MachineConfigPool{ObjectMeta: metav1.ObjectMeta{Name: "worker"}},
		Rendered: &mcfgv1.MachineConfig{ObjectMeta: metav1.ObjectMeta{Name: "rendered-worker-abc"}},
		Origin: cluster.ConfigurationOrigin{
			Kind:   cluster.ConfigurationCurrent,
			Source: "MCP status.configuration",
		},
		Path:     "/etc/ssh/sshd_config",
		Expected: []byte(contents),
		Found:    true,
		Mode:     &mode,
		Attribution: &attribution.Result{
			Path: "/etc/ssh/sshd_config",
			Writers: []attribution.Writer{
				{MachineConfigName: "00-worker"},
				{MachineConfigName: "99-worker-ssh"},
			},
			LastWriter: &attribution.Writer{MachineConfigName: "99-worker-ssh"},
		},
	}
}
