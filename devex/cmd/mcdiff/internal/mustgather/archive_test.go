package mustgather

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

const (
	sshdPath   = "/etc/ssh/sshd_config"
	renderedMC = "rendered-worker-abc"
)

func TestOpenRejectsMissingAndNonDir(t *testing.T) {
	t.Parallel()

	_, err := Open(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)

	f := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))
	_, err = Open(f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")

	_, err = Open(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not look like a must-gather")
}

func TestOpenNestedImageDir(t *testing.T) {
	t.Parallel()

	outer := t.TempDir()
	inner := filepath.Join(outer, "quay-io-must-gather")
	writePoolFixture(t, inner, "PermitRootLogin no\n", nil)

	mg, err := Open(outer)
	require.NoError(t, err)
	assert.Equal(t, outer, mg.Path)
	assert.Equal(t, inner, mg.Root)
}

func TestClusterGetterLoadPoolFile(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "canonical-from-render\n", nil)
	mg, err := Open(root)
	require.NoError(t, err)

	got, err := cluster.LoadPoolFile(context.Background(), mg.Getter(), "worker", sshdPath)
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, []byte("canonical-from-render\n"), got.Expected)
	assert.Equal(t, []string{"00-worker", "99-worker-ssh"}, got.WriterNames())
	assert.Equal(t, "99-worker-ssh", got.LastWriterName())
	assert.Equal(t, cluster.ConfigurationCurrent, got.Origin.Kind)
}

func TestClusterGetterPoolNotFound(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "x\n", nil)
	mg, err := Open(root)
	require.NoError(t, err)

	_, err = mg.Getter().GetMachineConfigPool(context.Background(), "infra")
	require.Error(t, err)
	assert.ErrorIs(t, err, cluster.ErrPoolNotFound)
}

func TestClusterGetterRenderedNotFound(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "x\n", nil)
	require.NoError(t, os.Remove(filepath.Join(root, "cluster-scoped-resources/machineconfiguration.openshift.io/machineconfigs", renderedMC+".yaml")))

	mg, err := Open(root)
	require.NoError(t, err)
	_, err = cluster.LoadPoolFile(context.Background(), mg.Getter(), "worker", sshdPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, cluster.ErrRenderedNotFound)
}

func TestClusterGetterJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writePoolFixture(t, root, "from-json\n", nil)
	mcDir := filepath.Join(root, "cluster-scoped-resources/machineconfiguration.openshift.io/machineconfigs")
	yamlPath := filepath.Join(mcDir, "00-worker.yaml")
	data, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	var mc mcfgv1.MachineConfig
	require.NoError(t, yaml.Unmarshal(data, &mc))
	js, err := json.Marshal(mc)
	require.NoError(t, err)
	require.NoError(t, os.Remove(yamlPath))
	require.NoError(t, os.WriteFile(filepath.Join(mcDir, "00-worker.json"), js, 0o600))

	mg, err := Open(root)
	require.NoError(t, err)
	got, err := mg.Getter().GetMachineConfig(context.Background(), "00-worker")
	require.NoError(t, err)
	assert.Equal(t, "00-worker", got.Name)
}

func TestClusterGetterMissingMachineConfigIsNotFound(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "x\n", nil)
	mg, err := Open(root)
	require.NoError(t, err)
	_, err = mg.Getter().GetMachineConfig(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err) || err != nil)
	assert.ErrorIs(t, err, cluster.ErrRenderedNotFound)
}

func TestNodeReaderHostSnapshot(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "expected\n", map[string]string{
		"worker-0": "actual-on-node\n",
	})
	mg, err := Open(root)
	require.NoError(t, err)

	content, mode, err := mg.NodeReader().ReadFile(context.Background(), "worker-0", sshdPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("actual-on-node\n"), content)
	require.NotNil(t, mode)
}

func TestNodeReaderFromCurrentConfig(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "expected\n", nil)
	ondisk := filepath.Join(root, "machine_config_ondisk", "worker-1")
	require.NoError(t, os.MkdirAll(ondisk, 0o755))
	writeYAML(t, filepath.Join(ondisk, "currentconfig"), mcWithFile(t, "current-worker-1", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-currentconfig\n"))

	mg, err := Open(root)
	require.NoError(t, err)
	content, _, err := mg.NodeReader().ReadFile(context.Background(), "worker-1", sshdPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("from-currentconfig\n"), content)
}

func TestNodeReaderMissingFile(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "expected\n", map[string]string{"worker-0": "x\n"})
	mg, err := Open(root)
	require.NoError(t, err)
	_, _, err = mg.NodeReader().ReadFile(context.Background(), "worker-0", "/etc/missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, node.ErrFileNotFound)
}

func TestNodeReaderNodeNotFound(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "expected\n", nil)
	mg, err := Open(root)
	require.NoError(t, err)
	_, _, err = mg.NodeReader().ReadFile(context.Background(), "no-such-node", sshdPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, node.ErrNodeNotFound)
}

func TestLoadPoolFileUnmanagedPath(t *testing.T) {
	t.Parallel()

	root := writePoolFixture(t, t.TempDir(), "present\n", nil)
	mg, err := Open(root)
	require.NoError(t, err)
	got, err := cluster.LoadPoolFile(context.Background(), mg.Getter(), "worker", "/etc/example")
	require.NoError(t, err)
	assert.False(t, got.Found)
}

func writePoolFixture(t *testing.T, root, renderedContents string, nodeFiles map[string]string) string {
	t.Helper()
	mcDir := filepath.Join(root, "cluster-scoped-resources", "machineconfiguration.openshift.io", "machineconfigs")
	poolDir := filepath.Join(root, "cluster-scoped-resources", "machineconfiguration.openshift.io", "machineconfigpools")
	nodeDir := filepath.Join(root, "cluster-scoped-resources", "core", "nodes")
	require.NoError(t, os.MkdirAll(mcDir, 0o755))
	require.NoError(t, os.MkdirAll(poolDir, 0o755))
	require.NoError(t, os.MkdirAll(nodeDir, 0o755))

	writeYAML(t, filepath.Join(mcDir, "00-worker.yaml"), mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-00\n"))
	writeYAML(t, filepath.Join(mcDir, "99-worker-ssh.yaml"), mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n"))
	writeYAML(t, filepath.Join(mcDir, renderedMC+".yaml"), mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, renderedContents))
	writeYAML(t, filepath.Join(poolDir, "worker.yaml"), mcpWithSources(t, "worker", renderedMC, "99-worker-ssh", "00-worker"))

	for name, contents := range nodeFiles {
		writeYAML(t, filepath.Join(nodeDir, name+".yaml"), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
		host := filepath.Join(root, "nodes", name, "host", "etc", "ssh")
		require.NoError(t, os.MkdirAll(host, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(host, "sshd_config"), []byte(contents), 0o644))
	}
	return root
}

func writeYAML(t *testing.T, path string, obj any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
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
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.GroupVersion.String(), Kind: "MachineConfigPool"},
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec:       mcfgv1.MachineConfigPoolSpec{Configuration: cfg},
		Status:     mcfgv1.MachineConfigPoolStatus{Configuration: cfg},
	}
}

func mcWithFile(t *testing.T, name, role, path, contents string) *mcfgv1.MachineConfig {
	t.Helper()
	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage: ign3types.Storage{
			Files: []ign3types.File{ctrlcommon.NewIgnFileBytes(path, []byte(contents))},
		},
	}
	raw, err := json.Marshal(ign)
	require.NoError(t, err)
	return &mcfgv1.MachineConfig{
		TypeMeta: metav1.TypeMeta{APIVersion: mcfgv1.GroupVersion.String(), Kind: "MachineConfig"},
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
