package daemon

import (
	"os"
	"strconv"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/vincent-petithory/dataurl"
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

	// validate single file
	files := []ignv2_2types.File{
		{
			Node: ignv2_2types.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: ignv2_2types.FileEmbedded1{
				Contents: ignv2_2types.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello world\n")),
				},
				Mode: &fileMode,
			},
		},
	}

	if status := checkFiles(files); !status {
		t.Errorf("Invalid files")
	}

	// validate overwritten file
	files = []ignv2_2types.File{
		{
			Node: ignv2_2types.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: ignv2_2types.FileEmbedded1{
				Contents: ignv2_2types.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello\n")),
				},
				Mode: &fileMode,
			},
		},
		{
			Node: ignv2_2types.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: ignv2_2types.FileEmbedded1{
				Contents: ignv2_2types.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello world\n")),
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
