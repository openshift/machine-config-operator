package scanner

import (
	"context"
	"encoding/json"
	"fmt"
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
)

const (
	sshdPath   = "/etc/ssh/sshd_config"
	regPath    = "/etc/containers/registries.conf"
	motdPath   = "/etc/motd"
	chronyPath = "/etc/chrony.conf"
	hostsPath  = "/etc/hosts"
	renderedMC = "rendered-worker-abc"
)

func TestScanAllMatch(t *testing.T) {
	t.Parallel()

	files := []pathContent{
		{sshdPath, "sshd\n"},
		{regPath, "registries\n"},
		{motdPath, "motd\n"},
		{chronyPath, "chrony\n"},
		{hostsPath, "hosts\n"},
	}
	g, reader := setupScan(t, files, map[string][]byte{
		sshdPath:   []byte("sshd\n"),
		regPath:    []byte("registries\n"),
		motdPath:   []byte("motd\n"),
		chronyPath: []byte("chrony\n"),
		hostsPath:  []byte("hosts\n"),
	})

	got, err := Scan(context.Background(), g, nil, reader, "worker-0", Options{Pool: "worker"})
	require.NoError(t, err)
	assert.Equal(t, 5, got.Scanned)
	assert.Equal(t, 5, got.Matching)
	assert.Equal(t, 0, got.Mismatched)
	assert.Equal(t, 0, got.Missing)
	assert.Equal(t, "clean", got.Status())
	assert.Equal(t, "worker", got.Pool)
	assert.Equal(t, renderedMC, got.Rendered)
	assert.Empty(t, got.MismatchedFiles)
	assert.Empty(t, got.MissingFiles)
}

func TestScanTwoOfFiveMismatch(t *testing.T) {
	t.Parallel()

	files := []pathContent{
		{sshdPath, "sshd-expected\n"},
		{regPath, "reg-expected\n"},
		{motdPath, "motd\n"},
		{chronyPath, "chrony\n"},
		{hostsPath, "hosts\n"},
	}
	g, reader := setupScan(t, files, map[string][]byte{
		sshdPath:   []byte("sshd-actual-longer\n"),
		regPath:    []byte("reg-actual-xx\n"),
		motdPath:   []byte("motd\n"),
		chronyPath: []byte("chrony\n"),
		hostsPath:  []byte("hosts\n"),
	})

	got, err := Scan(context.Background(), g, nil, reader, "worker-0", Options{Pool: "worker"})
	require.NoError(t, err)
	assert.Equal(t, 5, got.Scanned)
	assert.Equal(t, 3, got.Matching)
	assert.Equal(t, 2, got.Mismatched)
	assert.Equal(t, 0, got.Missing)
	assert.Equal(t, "drift", got.Status())

	require.Len(t, got.MismatchedFiles, 2)
	assert.Equal(t, regPath, got.MismatchedFiles[0].Path)
	assert.Equal(t, sshdPath, got.MismatchedFiles[1].Path)
	assert.Equal(t, "99-worker-ssh", got.MismatchedFiles[1].LastWriter)
	assert.NotEmpty(t, got.MismatchedFiles[0].Diff)
	assert.NotEqual(t, got.MismatchedFiles[0].ExpectedSize, got.MismatchedFiles[0].ActualSize)
}

func TestScanMissingFile(t *testing.T) {
	t.Parallel()

	files := []pathContent{
		{sshdPath, "sshd\n"},
		{motdPath, "hello\n"},
	}
	g, reader := setupScan(t, files, map[string][]byte{
		sshdPath: []byte("sshd\n"),
	})

	got, err := Scan(context.Background(), g, nil, reader, "worker-0", Options{Pool: "worker"})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Scanned)
	assert.Equal(t, 1, got.Matching)
	assert.Equal(t, 0, got.Mismatched)
	assert.Equal(t, 1, got.Missing)
	assert.Equal(t, "drift", got.Status())
	require.Len(t, got.MissingFiles, 1)
	assert.Equal(t, motdPath, got.MissingFiles[0].Path)
	assert.Equal(t, "00-worker", got.MissingFiles[0].LastWriter)
	assert.Equal(t, len("hello\n"), got.MissingFiles[0].ExpectedSize)
}

func TestScanModeMismatch(t *testing.T) {
	t.Parallel()

	files := []pathContent{{chronyPath, "pool 2.rhel.pool.ntp.org iburst\n"}}
	g, reader := setupScan(t, files, map[string][]byte{
		chronyPath: []byte("pool 2.rhel.pool.ntp.org iburst\n"),
	})
	reader.modes = map[string]int{chronyPath: 0o755}

	got, err := Scan(context.Background(), g, nil, reader, "worker-0", Options{Pool: "worker"})
	require.NoError(t, err)
	assert.Equal(t, 1, got.Scanned)
	assert.Equal(t, 0, got.Matching)
	assert.Equal(t, 1, got.Mismatched)
	assert.Equal(t, "drift", got.Status())
	require.Len(t, got.MismatchedFiles, 1)
	assert.True(t, got.MismatchedFiles[0].ModeMismatch)
	require.NotNil(t, got.MismatchedFiles[0].ExpectedMode)
	assert.Equal(t, 0o644, *got.MismatchedFiles[0].ExpectedMode)
	require.NotNil(t, got.MismatchedFiles[0].ActualMode)
	assert.Equal(t, 0o755, *got.MismatchedFiles[0].ActualMode)
	assert.Empty(t, got.MismatchedFiles[0].Diff)
}

func TestScanDetectsPoolFromNodeLabels(t *testing.T) {
	t.Parallel()

	files := []pathContent{{sshdPath, "sshd\n"}}
	g, reader := setupScan(t, files, map[string][]byte{sshdPath: []byte("sshd\n")})

	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "worker-0",
		Labels: map[string]string{"node-role.kubernetes.io/worker": ""},
	}}
	got, err := Scan(context.Background(), g, staticNode{n: n}, reader, "worker-0", Options{})
	require.NoError(t, err)
	assert.Equal(t, "worker", got.Pool)
	assert.Equal(t, 1, got.Matching)
}

func TestScanRequiresPoolWhenUnassigned(t *testing.T) {
	t.Parallel()

	files := []pathContent{{sshdPath, "sshd\n"}}
	g, reader := setupScan(t, files, map[string][]byte{sshdPath: []byte("sshd\n")})

	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0", Labels: map[string]string{"foo": "bar"}}}
	_, err := Scan(context.Background(), g, staticNode{n: n}, reader, "worker-0", Options{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNodeUnassigned)
}

func TestScanAbortsWhenNodeMissing(t *testing.T) {
	t.Parallel()

	files := []pathContent{{sshdPath, "sshd\n"}}
	g, _ := setupScan(t, files, nil)
	reader := &mapReader{err: fmt.Errorf("gone: %w", node.ErrNodeNotFound)}

	_, err := Scan(context.Background(), g, nil, reader, "worker-0", Options{Pool: "worker"})
	require.Error(t, err)
	assert.ErrorIs(t, err, node.ErrNodeNotFound)
}

type pathContent struct {
	path     string
	contents string
}

func setupScan(t *testing.T, files []pathContent, onDisk map[string][]byte) (cluster.Getter, *mapReader) {
	t.Helper()
	rendered := mcWithFiles(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, files...)
	sources := sourceConfigs(t, files)
	pool := mcpWithSources(t, "worker", renderedMC, sourceNames(sources)...)
	pool.Spec.NodeSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"node-role.kubernetes.io/worker": ""},
	}

	objs := []runtime.Object{pool, rendered}
	for _, s := range sources {
		objs = append(objs, s)
	}
	return cluster.NewKubeGetter(fake.NewSimpleClientset(objs...)), &mapReader{files: onDisk}
}

func sourceConfigs(t *testing.T, files []pathContent) []*mcfgv1.MachineConfig {
	t.Helper()
	var sources []*mcfgv1.MachineConfig
	byWriter := map[string][]pathContent{}
	for _, f := range files {
		writer := "00-worker"
		switch f.path {
		case sshdPath:
			writer = "99-worker-ssh"
		case regPath:
			writer = "99-worker-container-registry"
		}
		byWriter[writer] = append(byWriter[writer], f)
	}
	for name, list := range byWriter {
		sources = append(sources, mcWithFiles(t, name, ctrlcommon.MachineConfigPoolWorker, list...))
	}
	return sources
}

func sourceNames(sources []*mcfgv1.MachineConfig) []string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name)
	}
	return names
}

func mcWithFiles(t *testing.T, name, role string, files ...pathContent) *mcfgv1.MachineConfig {
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

type mapReader struct {
	files map[string][]byte
	modes map[string]int
	err   error
}

func (m *mapReader) ReadFile(_ context.Context, nodeName, path string) ([]byte, *int, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	b, ok := m.files[path]
	if !ok {
		return nil, nil, fmt.Errorf("file %q is missing on node %q: %w", path, nodeName, node.ErrFileNotFound)
	}
	if m.modes != nil {
		if mode, ok := m.modes[path]; ok {
			copied := mode
			return b, &copied, nil
		}
	}
	return b, nil, nil
}

type staticNode struct {
	n   *corev1.Node
	err error
}

func (s staticNode) GetNode(context.Context, string) (*corev1.Node, error) {
	return s.n, s.err
}
