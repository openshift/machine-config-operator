package report

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteScanClean(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := WriteScan(&buf, &scanner.Result{
		Node:     "worker-0",
		Pool:     "worker",
		Rendered: "rendered-worker-a1b2c3",
		Origin:   cluster.ConfigurationOrigin{Kind: cluster.ConfigurationCurrent, Source: "MCP status.configuration"},
		Scanned:  42,
		Matching: 42,
	}, ScanOptions{})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "Pool:             worker")
	assert.Contains(t, out, "Rendered MC:      rendered-worker-a1b2c3")
	assert.Contains(t, out, "Scanned Files:    42")
	assert.Contains(t, out, "Status:           CLEAN")
	assert.NotContains(t, out, "Mismatched Files:")
}

func TestWriteScanDrift(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := WriteScan(&buf, &scanner.Result{
		Node:       "worker-0",
		Pool:       "worker",
		Rendered:   "rendered-worker-a1b2c3",
		Scanned:    42,
		Matching:   39,
		Mismatched: 2,
		Missing:    1,
		MismatchedFiles: []scanner.Finding{
			{Path: "/etc/ssh/sshd_config", ExpectedSize: 3667, ActualSize: 3674, LastWriter: "99-worker-ssh", Diff: "-a\n+b\n"},
			{Path: "/etc/containers/registries.conf", ExpectedSize: 1200, ActualSize: 1250, LastWriter: "99-worker-container-registry"},
		},
		MissingFiles: []scanner.Finding{
			{Path: "/etc/motd", ExpectedSize: 12, LastWriter: "00-worker"},
		},
	}, ScanOptions{})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Status:           DRIFT DETECTED (2 files modified, 1 file missing)")
	assert.Contains(t, out, "1. /etc/ssh/sshd_config")
	assert.Contains(t, out, "Expected: 3667 bytes | Actual: 3674 bytes")
	assert.Contains(t, out, "Last Writer: 99-worker-ssh")
	assert.Contains(t, out, "1. /etc/motd")
	assert.Contains(t, out, "Status: MISSING ON NODE")
	assert.Contains(t, out, "Last Writer: 00-worker")
	assert.NotContains(t, out, "Unified diff:")
}

func TestWriteScanJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := WriteScan(&buf, &scanner.Result{
		Node:       "worker-0",
		Pool:       "worker",
		Rendered:   "rendered-worker-a1b2c3",
		Scanned:    3,
		Matching:   1,
		Mismatched: 1,
		Missing:    1,
		MismatchedFiles: []scanner.Finding{
			{Path: "/etc/ssh/sshd_config", ExpectedSize: 10, ActualSize: 12, LastWriter: "99-worker-ssh", Diff: "secret-diff"},
		},
		MissingFiles: []scanner.Finding{
			{Path: "/etc/motd", ExpectedSize: 5, LastWriter: "00-worker"},
		},
	}, ScanOptions{Format: "json"})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "drift", got["status"])
	assert.Equal(t, float64(3), got["scannedFiles"])
	item := got["mismatchedFiles"].([]any)[0].(map[string]any)
	_, hasDiff := item["diff"]
	assert.False(t, hasDiff)
}
