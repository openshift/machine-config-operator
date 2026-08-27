package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	sshdPath   = "/etc/ssh/sshd_config"
	renderedMC = "rendered-worker-abc"
)

func TestLoadPoolFileWorkerPool(t *testing.T) {
	t.Parallel()

	base := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-00\n")
	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n")
	// Would win a label-select re-merge; must be ignored because it is not in configuration.source.
	extra := mcWithFile(t, "zz-worker-extra", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-extra\n")
	// Authoritative expected bytes are on the rendered object, not a client-side merge.
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "canonical-from-render\n")
	pool := mcpWithSources(t, "worker", renderedMC, "99-worker-ssh", "00-worker")

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, base, overlay, extra, rendered), "worker", sshdPath)
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, []byte("canonical-from-render\n"), got.Expected)
	assert.Equal(t, []string{"00-worker", "99-worker-ssh"}, got.WriterNames())
	assert.Equal(t, "99-worker-ssh", got.LastWriterName())
	assert.NotContains(t, got.WriterNames(), "zz-worker-extra")
	assert.NotContains(t, got.WriterNames(), renderedMC)
	assert.Equal(t, ConfigurationCurrent, got.Origin.Kind)
	assert.Equal(t, "MCP status.configuration", got.Origin.Source)
}

func TestLoadPoolFileReversedSourceRefs(t *testing.T) {
	t.Parallel()

	base := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-00\n")
	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n")
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "canonical-from-render\n")
	pool := mcpWithSources(t, "worker", renderedMC, "00-worker", "99-worker-ssh")

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, overlay, base, rendered), "worker", sshdPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"00-worker", "99-worker-ssh"}, got.WriterNames())
	assert.Equal(t, "99-worker-ssh", got.LastWriterName())
}

func TestLoadPoolFileCustomPool(t *testing.T) {
	t.Parallel()

	worker := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-worker\n")
	infra := mcWithFile(t, "00-infra", "infra", sshdPath, "from-infra\n")
	rendered := mcWithFile(t, "rendered-infra-abc", "infra", sshdPath, "from-infra\n")
	pool := mcpWithSources(t, "infra", "rendered-infra-abc", "00-infra", "99-worker-ssh")

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, worker, infra, rendered), "infra", sshdPath)
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, []byte("from-infra\n"), got.Expected)
	assert.Equal(t, []string{"99-worker-ssh", "00-infra"}, got.WriterNames())
	assert.Equal(t, "00-infra", got.LastWriterName())
}

func TestLoadPoolFileAbsent(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "present\n")
	pool := mcpWithSources(t, "worker", renderedMC)

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, rendered), "worker", "/etc/example")
	require.NoError(t, err)
	assert.False(t, got.Found)
	assert.Empty(t, got.Expected)
}

func TestLoadPoolFileEmptyContents(t *testing.T) {
	t.Parallel()

	rendered := mcWithFileBytes(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, "/etc/empty", nil)
	pool := mcpWithSources(t, "worker", renderedMC)

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, rendered), "worker", "/etc/empty")
	require.NoError(t, err)
	assert.True(t, got.Found)
	assert.Equal(t, []byte{}, got.Expected)
}

func TestLoadPoolFileRenderedMissing(t *testing.T) {
	t.Parallel()

	pool := mcpWithSources(t, "worker", renderedMC)
	_, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool), "worker", sshdPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRenderedNotFound)
	assert.Contains(t, err.Error(), `failed to resolve rendered MachineConfig "rendered-worker-abc" for pool "worker"`)
}

func TestLoadPoolFileNoRenderedConfiguration(t *testing.T) {
	t.Parallel()

	pool := &mcfgv1.MachineConfigPool{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	_, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool), "worker", sshdPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoRenderedConfiguration)
	assert.Contains(t, err.Error(), `failed to resolve rendered MachineConfig for pool "worker"`)
}

func TestLoadPoolFileMissingSourceDoesNotDropExpected(t *testing.T) {
	t.Parallel()

	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-99\n")
	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "canonical-from-render\n")
	pool := mcpWithSources(t, "worker", renderedMC, "00-worker", "99-worker-ssh")

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, overlay, rendered), "worker", sshdPath)
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, []byte("canonical-from-render\n"), got.Expected)
	assert.Nil(t, got.Attribution)
	require.Error(t, got.AttributionErr)
	assert.ErrorIs(t, got.AttributionErr, ErrSourceUnavailable)
	assert.Contains(t, got.AttributionErr.Error(), "00-worker")
}

func TestLoadPoolFilePoolNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadPoolFile(context.Background(), newFakeGetter(t), "worker", sshdPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPoolNotFound)
}

func TestLoadPoolFilePrefersStatusConfiguration(t *testing.T) {
	t.Parallel()

	statusRendered := mcWithFile(t, "rendered-status", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-status\n")
	specRendered := mcWithFile(t, "rendered-spec", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-spec\n")
	pool := mcpWithSources(t, "worker", "rendered-spec")
	pool.Status.Configuration.Name = "rendered-status"
	pool.Status.Configuration.Source = nil

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, statusRendered, specRendered), "worker", sshdPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("from-status\n"), got.Expected)
	assert.Equal(t, "rendered-status", got.Rendered.Name)
	assert.Equal(t, ConfigurationCurrent, got.Origin.Kind)
	assert.Equal(t, "MCP status.configuration", got.Origin.Source)
}

func TestLoadPoolFileUsesSpecWhenStatusEmpty(t *testing.T) {
	t.Parallel()

	rendered := mcWithFile(t, renderedMC, ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-spec\n")
	pool := mcpWithSources(t, "worker", renderedMC)
	pool.Status.Configuration = mcfgv1.MachineConfigPoolStatusConfiguration{}

	got, err := LoadPoolFile(context.Background(), newFakeGetter(t, pool, rendered), "worker", sshdPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("from-spec\n"), got.Expected)
	assert.Equal(t, ConfigurationTarget, got.Origin.Kind)
	assert.Equal(t, "MCP spec.configuration", got.Origin.Source)
}

func newFakeGetter(t *testing.T, objs ...runtime.Object) Getter {
	t.Helper()
	return NewKubeGetter(fake.NewSimpleClientset(objs...))
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

func TestRenderedConfigurationEmpty(t *testing.T) {
	t.Parallel()
	_, _, err := renderedConfiguration(&mcfgv1.MachineConfigPool{})
	assert.ErrorIs(t, err, ErrNoRenderedConfiguration)
	assert.True(t, errors.Is(err, ErrNoRenderedConfiguration))
}
