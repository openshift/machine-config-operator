package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// symlinkedStore builds <tmp>/containers/storage -> <tmp>/kubelet/containers/storage.
// t.TempDir() can itself sit behind a symlink, so compare against resolvedTarget.
func symlinkedStore(t *testing.T) (link, resolvedTarget string) {
	t.Helper()
	tmp := t.TempDir()

	target := filepath.Join(tmp, "kubelet", "containers", "storage")
	require.NoError(t, os.MkdirAll(target, 0o755), "creating symlink target directory %s", target)

	linkParent := filepath.Join(tmp, "containers")
	require.NoError(t, os.MkdirAll(linkParent, 0o755), "creating symlink parent directory %s", linkParent)

	link = filepath.Join(linkParent, "storage")
	require.NoError(t, os.Symlink(target, link), "symlinking %s to %s", link, target)

	resolvedTarget, err := filepath.EvalSymlinks(target)
	require.NoError(t, err, "resolving symlink target %s", target)
	return link, resolvedTarget
}

func TestCanonicalizeGraphRoot(t *testing.T) {
	link, resolved := symlinkedStore(t)

	assert.Equal(t, resolved, canonicalizeGraphRoot(link), "symlinked graph root should resolve to its target")
	assert.Equal(t, resolved, canonicalizeGraphRoot(resolved), "already-resolved graph root should be unchanged")

	// Not filepath.Join, which Cleans its own result and would make this vacuous.
	unclean := t.TempDir() + "/absent/../absent/"
	require.NotEqual(t, filepath.Clean(unclean), unclean, "test input must actually need cleaning")
	assert.Equal(t, filepath.Clean(unclean), canonicalizeGraphRoot(unclean), "unresolvable path should fall back to a cleaned path")
}

func TestGenerateTransportPolicyKeyForReferenceResolvesGraphRoot(t *testing.T) {
	link, resolved := symlinkedStore(t)

	digest := "registry.example.com/os@sha256:" + strings.Repeat("a", 64)
	imageID := strings.Repeat("b", 64)

	r := &RpmOstreeClient{
		podmanInterface: &MockPodmanInterface{
			info: &PodmanInfo{Store: PodmanStorageConfig{GraphDriverName: "overlay", GraphRoot: link}},
		},
	}

	key, err := r.generateTransportPolicyKeyForReference(&PodmanImageInfo{RepoDigest: digest, ID: imageID})
	require.NoError(t, err, "generating policy key for graph root %s", link)

	assert.Equal(t, fmt.Sprintf("[overlay@%s]%s@%s", resolved, digest, imageID), key, "policy key should embed the resolved graph root")
	assert.NotContains(t, key, link, "unresolved graph root must not appear in the policy key")
}
