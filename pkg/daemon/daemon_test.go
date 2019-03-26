package daemon

import (
	"os"
	"strconv"
	"testing"

	igntypes "github.com/coreos/ignition/config/v3_0/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var pathtests = []struct {
	path    string
	isValid bool
}{
	{".good", true},
	{"./good", true},
	{"/good", true},
	{"../good", true},
	{"bad", false},
}

func TestValidPath(t *testing.T) {
	var isValid bool
	for _, tt := range pathtests {
		isValid = ValidPath(tt.path)
		if isValid != tt.isValid {
			t.Errorf("%s isValid should be %s, found %s", tt.path, strconv.FormatBool(tt.isValid), strconv.FormatBool(isValid))
		}
	}
}

func TestOverwrittenFile(t *testing.T) {
	fi, err := os.Lstat("fixtures/test1.txt")
	if err != nil {
		t.Errorf("Could not Lstat file: %v", err)
	}
	fileMode := int(fi.Mode().Perm())
	contentsSource1 := dataurl.EncodeBytes([]byte("hello world\n"))
	contentsSource2 := dataurl.EncodeBytes([]byte("hello\n"))

	// validate single file
	files := []igntypes.File{
		{
			Node: igntypes.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Contents: igntypes.FileContents{
					Source: &contentsSource1,
				},
				Mode: &fileMode,
			},
		},
	}

	if status := checkFiles(files); !status {
		t.Errorf("Invalid files")
	}

	// validate overwritten file
	files = []igntypes.File{
		{
			Node: igntypes.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Contents: igntypes.FileContents{
					Source: &contentsSource2,
				},
				Mode: &fileMode,
			},
		},
		{
			Node: igntypes.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Contents: igntypes.FileContents{
					Source: &contentsSource1,
				},
				Mode: &fileMode,
			},
		},
	}

	if status := checkFiles(files); !status {
		t.Errorf("Validating an overwritten file failed")
	}
}

func TestCompareOSImageURL(t *testing.T) {
	refA := "registry.example.com/foo/bar@sha256:0743a3cc3bcf3b4aabb814500c2739f84cb085ff4e7ec7996aef7977c4c19c7f"
	refB := "registry.example.com/foo/baz@sha256:0743a3cc3bcf3b4aabb814500c2739f84cb085ff4e7ec7996aef7977c4c19c7f"
	refC := "registry.example.com/foo/bar@sha256:2a76681fd15bfc06fa4aa0ff6913ba17527e075417fc92ea29f6bcc2afca24ff"
	m, err := compareOSImageURL(refA, refA)
	if !m {
		t.Fatalf("Expected refA ident")
	}
	m, err = compareOSImageURL(refA, refB)
	if !m {
		t.Fatalf("Expected refA = refB")
	}
	m, err = compareOSImageURL(refA, refC)
	if m {
		t.Fatalf("Expected refA != refC")
	}
	m, err = compareOSImageURL(refA, "registry.example.com/foo/bar")
	if m || err == nil {
		t.Fatalf("Expected err")
	}
}

func TestDaemonOnceFromNoPanic(t *testing.T) {
	exitCh := make(chan error)
	defer close(exitCh)
	stopCh := make(chan struct{})
	defer close(stopCh)

	// This is how a onceFrom daemon is initialized
	// and it shouldn't panic assuming kubeClient is there
	dn, err := New(
		"/",
		"testnodename",
		"testos",
		NewNodeUpdaterClient(),
		"test",
		nil,
		k8sfake.NewSimpleClientset(),
		false,
		"",
		nil,
		exitCh,
		stopCh,
	)
	require.Nil(t, err)
	require.NotPanics(t, func() { dn.triggerUpdateWithMachineConfig(&mcfgv1.MachineConfig{}, &mcfgv1.MachineConfig{}) })
}
