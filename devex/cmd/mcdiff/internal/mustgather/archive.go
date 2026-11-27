package mustgather

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

// errInvalidObjectName is returned when an archive lookup name is not a single
// path component. Names are joined onto the must-gather root, so separators
// and ".." would escape that root.
var errInvalidObjectName = errors.New("invalid object name")

var scopedDirNames = []string{"cluster-scoped-resources", "cluster-scopes"}

// MustGather is an unpacked oc adm must-gather tree.
type MustGather struct {
	// Path is the path the user passed to --must-gather.
	Path string
	// Root is the directory that contains cluster-scoped-resources (possibly a nested image dir).
	Root string
}

// Open verifies dir looks like a must-gather tree and returns an archive handle.
func Open(dir string) (*MustGather, error) {
	if dir == "" {
		return nil, fmt.Errorf("must-gather path must not be empty")
	}
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to open must-gather %q: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%q is not a directory; extract the must-gather archive first", dir)
	}

	root, err := resolveRoot(dir)
	if err != nil {
		return nil, err
	}
	klog.V(2).Info("using must-gather archive")
	return &MustGather{Path: dir, Root: root}, nil
}

// Getter returns a cluster.Getter backed by YAML/JSON manifests in the archive.
func (m *MustGather) Getter() *ClusterGetter {
	return &ClusterGetter{mg: m}
}

// NodeReader returns a node.Reader backed by host-file snapshots and on-disk MCD configs.
func (m *MustGather) NodeReader() *NodeReader {
	return &NodeReader{mg: m}
}

func resolveRoot(dir string) (string, error) {
	if isGatherRoot(dir) {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read must-gather %q: %w", dir, err)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		if isGatherRoot(candidate) {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("%q does not look like a must-gather (missing cluster-scoped-resources)", dir)
	default:
		// Several plugin images can land in one dest-dir; prefer one that has MCO CRs.
		for _, c := range found {
			if hasMCOResources(c) {
				return c, nil
			}
		}
		return found[0], nil
	}
}

func isGatherRoot(dir string) bool {
	for _, name := range scopedDirNames {
		st, err := os.Stat(filepath.Join(dir, name))
		if err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func hasMCOResources(root string) bool {
	for _, scoped := range scopedDirNames {
		p := filepath.Join(root, scoped, "machineconfiguration.openshift.io")
		st, err := os.Stat(p)
		if err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func (m *MustGather) scopedDirs() []string {
	var dirs []string
	for _, name := range scopedDirNames {
		p := filepath.Join(m.Root, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			dirs = append(dirs, p)
		}
	}
	return dirs
}

func validateObjectName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w %q", errInvalidObjectName, name)
	}
	return nil
}

func withinRoot(root, candidate string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candAbs, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(candAbs))
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path %q escapes must-gather root", candidate)
	}
	return nil
}

// readableFile returns candidate when it is a regular file inside root.
// Symlinks are followed only when the target is also a regular file inside root.
func (m *MustGather) readableFile(candidate string) (string, bool, error) {
	if m == nil {
		return "", false, fmt.Errorf("must-gather archive is not configured")
	}
	if err := withinRoot(m.Root, candidate); err != nil {
		return "", false, err
	}
	st, err := os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	pathToRead := candidate
	if st.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", false, err
		}
		if err := withinRoot(m.Root, resolved); err != nil {
			return "", false, err
		}
		st, err = os.Stat(resolved)
		if err != nil {
			return "", false, err
		}
		pathToRead = resolved
	}
	if !st.Mode().IsRegular() {
		return "", false, nil
	}
	return pathToRead, true, nil
}
