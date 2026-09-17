package daemon

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v2"
)

func TestParseVersion(t *testing.T) {
	verdata := `rpm-ostree:
  Version: '2022.10'
  Git: 6b302116c969397fd71899e3b9bb3b8c100d1af9
  Features:
   - rust
   - compose
   - rhsm
`
	var q RpmOstreeVersionData
	if err := yaml.UnmarshalStrict([]byte(verdata), &q); err != nil {
		panic(err)
	}

	assert.Equal(t, "2022.10", q.Root.Version)
	assert.Contains(t, q.Root.Features, "rust")
	assert.NotContains(t, q.Root.Features, "container")
}

// rpmOstreeVersionYAML builds a fake `rpm-ostree --version` YAML payload for
// the given Version field, mirroring real-world output.
func rpmOstreeVersionYAML(version string) []byte {
	return []byte(fmt.Sprintf(`rpm-ostree:
  Version: '%s'
  Git: 6b302116c969397fd71899e3b9bb3b8c100d1af9
  Features:
   - container
`, version))
}

func TestSupportsContainerStorageRebase(t *testing.T) {
	tests := []struct {
		name     string
		output   []byte
		cmdErr   error
		expected bool
	}{
		{
			// The exact version shipped in both the 4.13 and 4.14 RHCOS boot
			// images (confirmed via their commitmeta.json rpmdb.pkglist:
			// rpm-ostree-2023.3-1.el9_2 and rpm-ostree-2023.3-2.el9_2
			// respectively), which reproduces OCPBUGS-86768.
			name:     "4.13/4.14 shipped version is not new enough",
			output:   rpmOstreeVersionYAML("2023.3"),
			expected: false,
		},
		{
			name:     "well below the fixed version",
			output:   rpmOstreeVersionYAML("2022.10"),
			expected: false,
		},
		{
			name:     "exactly the fixed version",
			output:   rpmOstreeVersionYAML("2023.5"),
			expected: true,
		},
		{
			name:     "above the fixed version",
			output:   rpmOstreeVersionYAML("2023.6"),
			expected: true,
		},
		{
			name:     "unparseable version defaults to not supported",
			output:   rpmOstreeVersionYAML("not-a-version"),
			expected: false,
		},
		{
			name:     "command execution failure defaults to not supported",
			cmdErr:   errors.New("rpm-ostree: command not found"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// SupportsContainerStorageRebase caches its result behind a
			// package-level sync.Once (matching podmanSupportsSigstore and
			// skopeoVersionSupportsMultiArchSigstore); reset both the guard
			// and the cached value so each case is independently evaluated.
			rpmOstreeContainerStorageRebaseChecked = sync.Once{}
			rpmOstreeContainerStorageRebaseSupported = false

			mock := &MockCommandRunner{
				outputs: map[string][]byte{},
				errors:  map[string]error{},
			}
			if tt.cmdErr != nil {
				mock.errors["rpm-ostree --version"] = tt.cmdErr
			} else {
				mock.outputs["rpm-ostree --version"] = tt.output
			}

			r := &RpmOstreeClient{commandRunner: mock}
			assert.Equal(t, tt.expected, r.SupportsContainerStorageRebase())
		})
	}
}

func TestNormalizeCalVerForSemver(t *testing.T) {
	assert.Equal(t, "2023.3.0", normalizeCalVerForSemver("2023.3"))
	assert.Equal(t, "2023.5.0", normalizeCalVerForSemver("2023.5"))
	// Already dotted-tri (or with more components): left untouched.
	assert.Equal(t, "2023.5.1", normalizeCalVerForSemver("2023.5.1"))
}
