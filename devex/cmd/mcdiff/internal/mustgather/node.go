package mustgather

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// NodeReader reads host files from a must-gather archive.
type NodeReader struct {
	mg *MustGather
}

var _ node.Reader = (*NodeReader)(nil)
var _ node.Getter = (*MustGather)(nil)

// GetNode loads a Node object from the must-gather archive. Used to detect the
// node's MachineConfigPool from labels when --pool is not set.
func (m *MustGather) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	_ = ctx
	if m == nil {
		return nil, fmt.Errorf("must-gather archive is not configured")
	}
	p, err := m.findClusterObject("core", "nodes", name)
	if err != nil {
		return nil, fmt.Errorf("failed to get node %q from must-gather: %w: %w", name, node.ErrNodeNotFound, err)
	}
	var n corev1.Node
	if err := decodeFile(p, &n); err != nil {
		return nil, fmt.Errorf("failed to decode node %q from %s: %w", name, p, err)
	}
	if n.Name == "" {
		n.Name = name
	}
	klog.V(4).Infof("loaded Node %s from %s", name, p)
	return &n, nil
}

func (r *NodeReader) ReadFile(ctx context.Context, nodeName, filePath string) ([]byte, *int, error) {
	_ = ctx
	if r == nil || r.mg == nil {
		return nil, nil, fmt.Errorf("must-gather node reader is not configured")
	}
	if nodeName == "" {
		return nil, nil, fmt.Errorf("node name must not be empty")
	}
	if filePath == "" || !strings.HasPrefix(filePath, "/") {
		return nil, nil, fmt.Errorf("path %q must be an absolute Unix path", filePath)
	}

	if p := r.mg.hostSnapshot(nodeName, filePath); p != "" {
		klog.V(2).Infof("reading %s for node %s from must-gather snapshot %s", filePath, nodeName, p)
		return readHostSnapshot(p)
	}

	if r.mg.nodePresent(nodeName) {
		return nil, nil, fmt.Errorf("file %q is missing on node %q in must-gather: %w", filePath, nodeName, node.ErrFileNotFound)
	}
	return nil, nil, fmt.Errorf("node %q not found in must-gather: %w", nodeName, node.ErrNodeNotFound)
}

func (m *MustGather) hostSnapshot(nodeName, filePath string) string {
	rel := strings.TrimPrefix(path.Clean(filePath), "/")
	return existingFile(
		filepath.Join(m.Root, "nodes", nodeName, "host", rel),
		filepath.Join(m.Root, "host_files", nodeName, rel),
		filepath.Join(m.Root, "machine_config_ondisk", nodeName, "files", rel),
	)
}

func (m *MustGather) nodePresent(nodeName string) bool {
	if _, err := m.findClusterObject("core", "nodes", nodeName); err == nil {
		return true
	}
	dirs := []string{
		filepath.Join(m.Root, "nodes", nodeName),
		filepath.Join(m.Root, "host_files", nodeName),
		filepath.Join(m.Root, "machine_config_ondisk", nodeName),
	}
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func readHostSnapshot(p string) ([]byte, *int, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return data, nil, nil
	}
	mode := int(info.Mode().Perm())
	if data == nil {
		data = []byte{}
	}
	return data, &mode, nil
}
