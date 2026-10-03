package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRunNodeAllMatch(t *testing.T) {
	t.Parallel()

	g, reader := nodeScanFixture(t, map[string]string{
		sshdPath: "sshd\n",
		motdPath: "hello\n",
	}, map[string][]byte{
		sshdPath: []byte("sshd\n"),
		motdPath: []byte("hello\n"),
	})

	var buf bytes.Buffer
	err := runNode(context.Background(), g, nil, reader, nodeScanArgs{node: "worker-0", pool: "worker", output: "text"}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Node:             worker-0")
	assert.Contains(t, out, "Pool:             worker")
	assert.Contains(t, out, "Rendered MC:      rendered-worker-abc")
	assert.Contains(t, out, "Scanned Files:    2")
	assert.Contains(t, out, "Status:           CLEAN")
	assert.NotContains(t, out, "Mismatched Files:")
	assert.NotContains(t, out, "sshd\n")
}

func TestRunNodeMismatchesAndMissing(t *testing.T) {
	t.Parallel()

	g, reader := nodeScanFixture(t, map[string]string{
		sshdPath: "PermitRootLogin no\n",
		regPath:  "unqualified-search-registries = []\n",
		motdPath: "hello\n",
	}, map[string][]byte{
		sshdPath: []byte("PermitRootLogin yes\n"),
		regPath:  []byte("unqualified-search-registries = ['example.com']\n"),
	})

	var buf bytes.Buffer
	err := runNode(context.Background(), g, nil, reader, nodeScanArgs{node: "worker-0", pool: "worker", output: "text"}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Status:           DRIFT DETECTED (2 files modified, 1 file missing)")
	assert.Contains(t, out, "Mismatched Files:")
	assert.Contains(t, out, "/etc/ssh/sshd_config")
	assert.Contains(t, out, "/etc/containers/registries.conf")
	assert.Contains(t, out, "Last Writer: 99-worker-ssh")
	assert.Contains(t, out, "Missing Files:")
	assert.Contains(t, out, "/etc/motd")
	assert.NotContains(t, out, "Unified diff:")
	assert.NotContains(t, out, "PermitRootLogin yes")
}

func TestRunNodeShowDiffs(t *testing.T) {
	t.Parallel()

	g, reader := nodeScanFixture(t, map[string]string{sshdPath: "PermitRootLogin no\n"}, map[string][]byte{sshdPath: []byte("PermitRootLogin yes\n")})
	var buf bytes.Buffer
	err := runNode(context.Background(), g, nil, reader, nodeScanArgs{node: "worker-0", pool: "worker", output: "text", showDiffs: true}, &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Unified diff:")
	assert.Contains(t, out, "-PermitRootLogin no")
	assert.Contains(t, out, "+PermitRootLogin yes")
}

func TestRunNodeJSON(t *testing.T) {
	t.Parallel()

	g, reader := nodeScanFixture(t, map[string]string{
		sshdPath: "sshd-expected\n",
		motdPath: "hello\n",
	}, map[string][]byte{
		sshdPath: []byte("sshd-actual-xx\n"),
	})

	var buf bytes.Buffer
	err := runNode(context.Background(), g, nil, reader, nodeScanArgs{node: "worker-0", pool: "worker", output: "json"}, &buf)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "worker-0", got["node"])
	assert.Equal(t, "worker", got["pool"])
	assert.Equal(t, renderedMC, got["renderedMachineConfig"])
	assert.Equal(t, float64(2), got["scannedFiles"])
	assert.Equal(t, float64(0), got["matching"])
	assert.Equal(t, float64(1), got["mismatched"])
	assert.Equal(t, float64(1), got["missing"])
	assert.Equal(t, "drift", got["status"])
	assert.Equal(t, "current", got["configuration"])

	mismatched, ok := got["mismatchedFiles"].([]any)
	require.True(t, ok)
	require.Len(t, mismatched, 1)
	item := mismatched[0].(map[string]any)
	assert.Equal(t, sshdPath, item["path"])
	assert.Equal(t, "99-worker-ssh", item["lastWriter"])
	assert.NotNil(t, item["actualSize"])
	_, hasDiff := item["diff"]
	assert.False(t, hasDiff, "unified diffs are omitted from JSON unless --show-diffs")

	missing, ok := got["missingFiles"].([]any)
	require.True(t, ok)
	require.Len(t, missing, 1)
	miss := missing[0].(map[string]any)
	assert.Equal(t, motdPath, miss["path"])
	assert.Equal(t, "00-worker", miss["lastWriter"])
	_, hasActual := miss["actualSize"]
	assert.False(t, hasActual)
}

func TestRunNodeJSONShowDiffs(t *testing.T) {
	t.Parallel()

	g, reader := nodeScanFixture(t, map[string]string{sshdPath: "no\n"}, map[string][]byte{sshdPath: []byte("yes\n")})
	var buf bytes.Buffer
	err := runNode(context.Background(), g, nil, reader, nodeScanArgs{node: "worker-0", pool: "worker", output: "json", showDiffs: true}, &buf)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	item := got["mismatchedFiles"].([]any)[0].(map[string]any)
	diffStr, ok := item["diff"].(string)
	require.True(t, ok)
	assert.Contains(t, diffStr, "-no")
	assert.Contains(t, diffStr, "+yes")
}

func TestRunNodeMustGather(t *testing.T) {
	t.Parallel()

	dir := writeMustGatherFixture(t, "PermitRootLogin no\n", map[string]string{"worker-0": "PermitRootLogin yes\n"})
	var buf bytes.Buffer
	o := &nodeOptions{pool: "worker", mustGather: dir, output: "text", out: &buf}
	err := o.run(context.Background(), "worker-0")
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Archive:          Must-Gather Archive ("+dir+")")
	assert.Contains(t, out, "DRIFT DETECTED")
	assert.Contains(t, out, sshdPath)
}

func TestNodeCommandHelp(t *testing.T) {
	t.Parallel()

	cmd := newNodeCommand()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "mcdiff node worker-0")
	assert.Contains(t, out, "--pool")
	assert.Contains(t, out, "--show-diffs")
	assert.Contains(t, out, "--must-gather")
	assert.Contains(t, out, "-o json")
}

const (
	motdPath = "/etc/motd"
	regPath  = "/etc/containers/registries.conf"
)

func nodeScanFixture(t *testing.T, expected map[string]string, actual map[string][]byte) (cluster.Getter, node.Reader) {
	t.Helper()
	var files []ignFile
	for path, contents := range expected {
		files = append(files, ignFile{path, contents})
	}
	rendered := mcWithFiles(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, files...)
	var sources []runtime.Object
	var sourceNames []string
	for path, contents := range expected {
		name := "00-worker"
		switch path {
		case sshdPath:
			name = "99-worker-ssh"
		case regPath:
			name = "99-worker-container-registry"
		}
		sources = append(sources, mcWithFile(t, name, ctrlcommon.MachineConfigPoolWorker, path, contents))
		sourceNames = append(sourceNames, name)
	}
	pool := mcpWithSources(t, "worker", renderedMC, sourceNames...)
	objs := []runtime.Object{pool, rendered}
	objs = append(objs, sources...)
	return newFakeGetter(t, objs...), &pathReader{files: actual}
}

type ignFile struct {
	path     string
	contents string
}

func mcWithFiles(t *testing.T, name, role string, files ...ignFile) *mcfgv1.MachineConfig {
	t.Helper()
	ignFiles := make([]ign3types.File, 0, len(files))
	for _, f := range files {
		ignFiles = append(ignFiles, ctrlcommon.NewIgnFileBytes(f.path, []byte(f.contents)))
	}
	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage:  ign3types.Storage{Files: ignFiles},
	}
	raw, err := json.Marshal(ign)
	require.NoError(t, err)
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: role},
		},
		Spec: mcfgv1.MachineConfigSpec{Config: runtime.RawExtension{Raw: raw}},
	}
}

type pathReader struct {
	files map[string][]byte
}

func (p *pathReader) ReadFile(_ context.Context, nodeName, path string) ([]byte, *int, error) {
	b, ok := p.files[path]
	if !ok {
		return nil, nil, fmt.Errorf("file %q is missing on node %q: %w", path, nodeName, node.ErrFileNotFound)
	}
	return b, nil, nil
}
