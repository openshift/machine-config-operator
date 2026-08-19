package scanner

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

const osLabel = "kubernetes.io/os"

// ResolvePrimaryPool returns the MachineConfigPool a node targets, using the
// same rules as pkg/helpers.GetPrimaryPoolForNode: custom pool beats worker,
// master beats custom and worker, multiple custom pools are an error.
func ResolvePrimaryPool(n *corev1.Node, pools []*mcfgv1.MachineConfigPool) (*mcfgv1.MachineConfigPool, error) {
	if n == nil {
		return nil, fmt.Errorf("node is nil")
	}
	if isWindows(n) {
		return nil, fmt.Errorf("node %q is a Windows node and is not managed by the Machine Config Operator; pass --pool to override: %w", n.Name, ErrWindowsNode)
	}

	master, worker, custom, err := matchingPools(n, pools)
	if err != nil {
		return nil, err
	}
	if master == nil && worker == nil && len(custom) == 0 {
		return nil, fmt.Errorf("node %q is not assigned to a MachineConfigPool; pass --pool: %w", n.Name, ErrNodeUnassigned)
	}

	switch {
	case len(custom) > 1:
		return nil, fmt.Errorf("node %q belongs to %d custom MachineConfigPools; pass --pool to select one: %w", n.Name, len(custom), ErrMultipleCustomPools)
	case len(custom) == 1:
		if master != nil {
			klog.V(2).Infof("node %s matches master and custom pool %s; defaulting to master", n.Name, custom[0].Name)
			return master, nil
		}
		return custom[0], nil
	case master != nil:
		return master, nil
	default:
		return worker, nil
	}
}

func matchingPools(n *corev1.Node, pools []*mcfgv1.MachineConfigPool) (*mcfgv1.MachineConfigPool, *mcfgv1.MachineConfigPool, []*mcfgv1.MachineConfigPool, error) {
	var matched []*mcfgv1.MachineConfigPool
	for _, p := range pools {
		if p == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid node selector on MachineConfigPool %s: %w", p.Name, err)
		}
		if selector.Empty() || !selector.Matches(labels.Set(n.Labels)) {
			continue
		}
		matched = append(matched, p)
	}

	var master, worker *mcfgv1.MachineConfigPool
	var custom []*mcfgv1.MachineConfigPool
	for _, pool := range matched {
		switch pool.Name {
		case ctrlcommon.MachineConfigPoolMaster:
			master = pool
		case ctrlcommon.MachineConfigPoolWorker:
			worker = pool
		default:
			custom = append(custom, pool)
		}
	}
	return master, worker, custom, nil
}

func isWindows(n *corev1.Node) bool {
	if value, ok := n.Labels[osLabel]; ok {
		return value == "windows"
	}
	return false
}
