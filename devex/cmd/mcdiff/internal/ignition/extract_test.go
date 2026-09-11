package ignition

import (
	"encoding/base64"
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

func TestExtractAllMultipleFiles(t *testing.T) {
	t.Parallel()

	mc := mcWithFiles(t, "rendered-worker-abc",
		fileSpec{"/etc/a", []byte("a\n")},
		fileSpec{"/etc/b", []byte("b\n")},
		fileSpec{"/etc/empty", nil},
	)
	got, err := ExtractAll(mc)
	require.NoError(t, err)
	require.Len(t, got, 3)

	byPath := map[string]File{}
	for _, f := range got {
		byPath[f.Path] = f
	}
	assert.Equal(t, []byte("a\n"), byPath["/etc/a"].Contents)
	assert.Equal(t, []byte("b\n"), byPath["/etc/b"].Contents)
	assert.True(t, byPath["/etc/empty"].Found)
	assert.Equal(t, []byte{}, byPath["/etc/empty"].Contents)
	assert.NoError(t, byPath["/etc/a"].Err)
}

func TestExtractAllEmptyConfig(t *testing.T) {
	t.Parallel()

	got, err := ExtractAll(&mcfgv1.MachineConfig{ObjectMeta: metav1.ObjectMeta{Name: "empty"}})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestExtractAllNil(t *testing.T) {
	t.Parallel()
	_, err := ExtractAll(nil)
	require.Error(t, err)
}

func TestExtractFileBase64DataURL(t *testing.T) {
	t.Parallel()

	want := []byte("pool 2.rhel.pool.ntp.org iburst\n")
	src := "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(want)
	mc := mcWithIgnSource(t, "/etc/chrony.conf", src)

	got, err := ExtractFile(mc, "/etc/chrony.conf")
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, want, got.Contents)
}

func TestExtractFilePercentEncodedDataURL(t *testing.T) {
	t.Parallel()

	want := []byte("pool 2.rhel.pool.ntp.org iburst\n")
	src := "data:,pool%202.rhel.pool.ntp.org%20iburst%0A"
	mc := mcWithIgnSource(t, "/etc/chrony.conf", src)

	got, err := ExtractFile(mc, "/etc/chrony.conf")
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, want, got.Contents)
}

func TestExtractAllDecodesBothEncodings(t *testing.T) {
	t.Parallel()

	chrony := []byte("pool 2.rhel.pool.ntp.org iburst\n")
	resolv := []byte("nameserver 1.1.1.1\n")
	mode := 0o644
	empty := ""
	b64 := "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(chrony)
	pct := "data:,nameserver%201.1.1.1%0A"
	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage: ign3types.Storage{
			Files: []ign3types.File{
				{
					Node: ign3types.Node{Path: "/etc/chrony.conf"},
					FileEmbedded1: ign3types.FileEmbedded1{
						Mode: &mode,
						Contents: ign3types.Resource{
							Source:      &b64,
							Compression: &empty,
						},
					},
				},
				{
					Node: ign3types.Node{Path: "/etc/resolv.conf"},
					FileEmbedded1: ign3types.FileEmbedded1{
						Mode: &mode,
						Contents: ign3types.Resource{
							Source:      &pct,
							Compression: &empty,
						},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(ign)
	require.NoError(t, err)
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "rendered-worker-abc"},
		Spec:       mcfgv1.MachineConfigSpec{Config: runtime.RawExtension{Raw: raw}},
	}

	got, err := ExtractAll(mc)
	require.NoError(t, err)
	byPath := map[string]File{}
	for _, f := range got {
		byPath[f.Path] = f
	}
	assert.Equal(t, chrony, byPath["/etc/chrony.conf"].Contents)
	assert.Equal(t, resolv, byPath["/etc/resolv.conf"].Contents)
}

func mcWithIgnSource(t *testing.T, path, source string) *mcfgv1.MachineConfig {
	t.Helper()
	mode := 0o644
	empty := ""
	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage: ign3types.Storage{
			Files: []ign3types.File{{
				Node: ign3types.Node{Path: path},
				FileEmbedded1: ign3types.FileEmbedded1{
					Mode: &mode,
					Contents: ign3types.Resource{
						Source:      &source,
						Compression: &empty,
					},
				},
			}},
		},
	}
	raw, err := json.Marshal(ign)
	require.NoError(t, err)
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "rendered-worker-abc"},
		Spec:       mcfgv1.MachineConfigSpec{Config: runtime.RawExtension{Raw: raw}},
	}
}

type fileSpec struct {
	path     string
	contents []byte
}

func mcWithFiles(t *testing.T, name string, files ...fileSpec) *mcfgv1.MachineConfig {
	t.Helper()
	ignFiles := make([]ign3types.File, 0, len(files))
	for _, f := range files {
		contents := f.contents
		if contents == nil {
			contents = []byte{}
		}
		ignFiles = append(ignFiles, ctrlcommon.NewIgnFileBytes(f.path, contents))
	}
	ign := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Storage:  ign3types.Storage{Files: ignFiles},
	}
	raw, err := json.Marshal(ign)
	require.NoError(t, err)
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mcfgv1.MachineConfigSpec{Config: runtime.RawExtension{Raw: raw}},
	}
}
