package attribution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const sshdPath = "/etc/ssh/sshd_config"

func TestAttributeLastWriterWinsAmongWorkerFragments(t *testing.T) {
	t.Parallel()

	base := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\n")
	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "PermitRootLogin no\nUsePAM yes\n")
	unrelated := mcWithFile(t, "01-worker-kubelet", ctrlcommon.MachineConfigPoolWorker, "/etc/kubernetes/kubelet.conf", "kubelet\n")

	// Reverse name order to prove we sort the way MergeMachineConfigs does.
	got, err := Attribute(sshdPath, []*mcfgv1.MachineConfig{overlay, unrelated, base})
	require.NoError(t, err)
	require.NotNil(t, got.LastWriter)

	assert.Equal(t, sshdPath, got.Path)
	assert.Equal(t, []string{"00-worker", "99-worker-ssh"}, writerNames(got.Writers))
	assert.Equal(t, "99-worker-ssh", got.LastWriter.MachineConfigName)
	assert.Equal(t, sha256Hex("PermitRootLogin no\nUsePAM yes\n"), got.LastWriter.ContentSHA256)
	assert.Equal(t, sha256Hex("PermitRootLogin no\n"), got.Writers[0].ContentSHA256)
}

func TestAttributeCustomPoolOverridesWorker(t *testing.T) {
	t.Parallel()

	// Worker fragments are merged first (by name), then non-worker fragments.
	// A custom-pool MC named 00-infra still wins over 99-worker-ssh.
	worker := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "from-worker\n")
	infra := mcWithFile(t, "00-infra", "infra", sshdPath, "from-infra\n")

	got, err := Attribute(sshdPath, []*mcfgv1.MachineConfig{infra, worker})
	require.NoError(t, err)
	require.NotNil(t, got.LastWriter)

	assert.Equal(t, []string{"99-worker-ssh", "00-infra"}, writerNames(got.Writers))
	assert.Equal(t, "00-infra", got.LastWriter.MachineConfigName)
	assert.Equal(t, sha256Hex("from-infra\n"), got.LastWriter.ContentSHA256)
}

func TestAttributePathNotPresent(t *testing.T) {
	t.Parallel()

	mc := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, "/etc/hostname", "node\n")
	got, err := Attribute(sshdPath, []*mcfgv1.MachineConfig{mc})
	require.NoError(t, err)
	assert.Empty(t, got.Writers)
	assert.Nil(t, got.LastWriter)
}

func TestAttributeEmptySources(t *testing.T) {
	t.Parallel()

	got, err := Attribute(sshdPath, nil)
	require.NoError(t, err)
	assert.Empty(t, got.Writers)
	assert.Nil(t, got.LastWriter)
}

func TestAttributeRejectsEmptyPath(t *testing.T) {
	t.Parallel()

	_, err := Attribute("", []*mcfgv1.MachineConfig{})
	require.Error(t, err)
}

func TestAttributeRejectsMissingRoleLabel(t *testing.T) {
	t.Parallel()

	mc := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "x\n")
	mc.Labels = nil
	_, err := Attribute(sshdPath, []*mcfgv1.MachineConfig{mc})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find label")
}

func TestAttributeSkipsEmptyIgnition(t *testing.T) {
	t.Parallel()

	empty := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "00-worker-empty",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
	}
	overlay := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "only-overlay\n")

	got, err := Attribute(sshdPath, []*mcfgv1.MachineConfig{empty, overlay})
	require.NoError(t, err)
	require.NotNil(t, got.LastWriter)
	assert.Equal(t, []string{"99-worker-ssh"}, writerNames(got.Writers))
}

func TestSortForMergeDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	a := mcWithFile(t, "99-worker-ssh", ctrlcommon.MachineConfigPoolWorker, sshdPath, "a\n")
	b := mcWithFile(t, "00-worker", ctrlcommon.MachineConfigPoolWorker, sshdPath, "b\n")
	in := []*mcfgv1.MachineConfig{a, b}

	ordered, err := sortForMerge(in)
	require.NoError(t, err)
	require.Len(t, ordered, 2)
	assert.Equal(t, "00-worker", ordered[0].Name)
	assert.Equal(t, "99-worker-ssh", ordered[1].Name)
	assert.Equal(t, "99-worker-ssh", in[0].Name)
	assert.Equal(t, "00-worker", in[1].Name)
}

func mcWithFile(t *testing.T, name, role, path, contents string) *mcfgv1.MachineConfig {
	t.Helper()

	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage: ign3types.Storage{
			Files: []ign3types.File{ctrlcommon.NewIgnFile(path, contents)},
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

func writerNames(writers []Writer) []string {
	names := make([]string, 0, len(writers))
	for _, w := range writers {
		names = append(names, w.MachineConfigName)
	}
	return names
}

func sha256Hex(contents string) string {
	sum := sha256.Sum256([]byte(contents))
	return hex.EncodeToString(sum[:])
}
