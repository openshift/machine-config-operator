package mustgather

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// ClusterGetter loads MachineConfigPools and MachineConfigs from must-gather manifests.
type ClusterGetter struct {
	mg *MustGather
}

var _ cluster.Getter = (*ClusterGetter)(nil)

func (g *ClusterGetter) GetMachineConfigPool(ctx context.Context, name string) (*mcfgv1.MachineConfigPool, error) {
	_ = ctx
	if g == nil || g.mg == nil {
		return nil, fmt.Errorf("must-gather getter is not configured")
	}
	p, err := g.mg.findClusterObject("machineconfiguration.openshift.io", "machineconfigpools", name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MachineConfigPool %q: %w: %w", name, cluster.ErrPoolNotFound, err)
	}
	var pool mcfgv1.MachineConfigPool
	if err := decodeFile(p, &pool); err != nil {
		return nil, fmt.Errorf("failed to decode MachineConfigPool %q from %s: %w", name, p, err)
	}
	if pool.Name == "" {
		pool.Name = name
	}
	klog.V(4).Infof("loaded MachineConfigPool %s from %s", name, p)
	return &pool, nil
}

func (g *ClusterGetter) GetMachineConfig(ctx context.Context, name string) (*mcfgv1.MachineConfig, error) {
	_ = ctx
	if g == nil || g.mg == nil {
		return nil, fmt.Errorf("must-gather getter is not configured")
	}
	p, err := g.mg.findClusterObject("machineconfiguration.openshift.io", "machineconfigs", name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MachineConfig %q from must-gather: %w: %w", name, cluster.ErrRenderedNotFound, apierrors.NewNotFound(mcfgv1.Resource("machineconfigs"), name))
	}
	var mc mcfgv1.MachineConfig
	if err := decodeFile(p, &mc); err != nil {
		return nil, fmt.Errorf("failed to decode MachineConfig %q from %s: %w", name, p, err)
	}
	if mc.Name == "" {
		mc.Name = name
	}
	klog.V(4).Infof("loaded MachineConfig %s from %s", name, p)
	return &mc, nil
}

func (g *ClusterGetter) ListMachineConfigPools(ctx context.Context) ([]*mcfgv1.MachineConfigPool, error) {
	_ = ctx
	if g == nil || g.mg == nil {
		return nil, fmt.Errorf("must-gather getter is not configured")
	}
	paths, err := g.mg.listClusterObjects("machineconfiguration.openshift.io", "machineconfigpools")
	if err != nil {
		return nil, fmt.Errorf("failed to list MachineConfigPools from must-gather: %w", err)
	}
	out := make([]*mcfgv1.MachineConfigPool, 0, len(paths))
	for _, p := range paths {
		var pool mcfgv1.MachineConfigPool
		if err := decodeFile(p, &pool); err != nil {
			return nil, fmt.Errorf("failed to decode MachineConfigPool from %s: %w", p, err)
		}
		if pool.Name == "" {
			pool.Name = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		}
		out = append(out, &pool)
	}
	return out, nil
}

func (m *MustGather) findClusterObject(group, resource, name string) (string, error) {
	var tried []string
	for _, scoped := range m.scopedDirs() {
		base := filepath.Join(scoped, group, resource)
		for _, ext := range []string{".yaml", ".yml", ".json"} {
			candidate := filepath.Join(base, name+ext)
			tried = append(tried, candidate)
			if existingFile(candidate) != "" {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("not found (looked in %v)", tried)
}

func (m *MustGather) listClusterObjects(group, resource string) ([]string, error) {
	seen := map[string]struct{}{}
	var paths []string
	for _, scoped := range m.scopedDirs() {
		dir := filepath.Join(scoped, group, resource)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := filepath.Ext(e.Name())
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ext)
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths, nil
}

func decodeFile(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, into)
}
