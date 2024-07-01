package helpers

import (
	"fmt"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	v1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

const (
	// osLabel is used to identify which type of OS the node has
	OSLabel = "kubernetes.io/os"
)

func GetNodesForPool(mcpLister v1.MachineConfigPoolLister, nodeLister corev1listers.NodeLister, pool *mcfgv1.MachineConfigPool) ([]*corev1.Node, error) {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	initialNodes, err := nodeLister.List(selector)
	if err != nil {
		return nil, err
	}

	nodes := []*corev1.Node{}
	for _, n := range initialNodes {
		p, err := GetPrimaryPoolForNode(mcpLister, n)
		if err != nil {
			klog.Warningf("can't get pool for node %q: %v", n.Name, err)
			continue
		}
		if p == nil {
			continue
		}
		if p.Name != pool.Name {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func GetPrimaryPoolForNode(mcpLister v1.MachineConfigPoolLister, node *corev1.Node) (*mcfgv1.MachineConfigPool, error) {
	pools, _, err := GetPoolsForNode(mcpLister, node)
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return nil, nil
	}
	return pools[0], nil
}

func GetPoolsForNode(mcpLister v1.MachineConfigPoolLister, node *corev1.Node) ([]*mcfgv1.MachineConfigPool, *int, error) {
	var metricValue int
	master, worker, custom, err := ListPools(node, mcpLister)
	if err != nil {
		return nil, nil, err
	}
	if master == nil && custom == nil && worker == nil {
		return nil, nil, nil
	}

	switch {
	case len(custom) > 1:
		return nil, nil, fmt.Errorf("node %s belongs to %d custom roles, cannot proceed with this Node", node.Name, len(custom))
	case len(custom) == 1:
		pls := []*mcfgv1.MachineConfigPool{}
		if master != nil {
			// if we have a custom pool and master, defer to master and return.
			klog.Infof("Found master node that matches selector for custom pool %v, defaulting to master. This node will not have any custom role configuration as a result. Please review the node to make sure this is intended", custom[0].Name)
			metricValue = 1
			pls = append(pls, master)
		} else {
			metricValue = 0
			pls = append(pls, custom[0])
		}
		if worker != nil {
			pls = append(pls, worker)
		}
		// this allows us to have master, worker, infra but be in the master pool.
		// or if !worker and !master then we just use the custom pool.
		return pls, &metricValue, nil
	case master != nil:
		// In the case where a node is both master/worker, have it live under
		// the master pool. This occurs in CodeReadyContainers and general
		// "single node" deployments, which one may want to do for testing bare
		// metal, etc.
		metricValue = 0
		return []*mcfgv1.MachineConfigPool{master}, &metricValue, nil
	default:
		// Otherwise, it's a worker with no custom roles.
		metricValue = 0
		return []*mcfgv1.MachineConfigPool{worker}, &metricValue, nil
	}
}

func GetPoolNamesForNode(mcpLister v1.MachineConfigPoolLister, node *corev1.Node) ([]string, error) {
	pools, _, err := GetPoolsForNode(mcpLister, node)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, pool := range pools {
		names = append(names, pool.Name)
	}
	return names, nil
}

// isWindows checks if given node is a Windows node or a Linux node
func IsWindows(node *corev1.Node) bool {
	windowsOsValue := "windows"
	if value, ok := node.ObjectMeta.Labels[OSLabel]; ok {
		if value == windowsOsValue {
			return true
		}
		return false
	}
	// All the nodes should have a OS label populated by kubelet, if not just to maintain
	// backwards compatibility, we can returning true here.
	return false
}

func ListPools(node *corev1.Node, mcpLister v1.MachineConfigPoolLister) (*mcfgv1.MachineConfigPool, *mcfgv1.MachineConfigPool, []*mcfgv1.MachineConfigPool, error) {
	if IsWindows(node) {
		// This is not an error, is this a Windows Node and it won't be managed by MCO. We're explicitly logging
		// here at a high level to disambiguate this from other pools = nil  scenario
		klog.V(4).Infof("Node %v is a windows node so won't be managed by MCO", node.Name)
		return nil, nil, nil, nil
	}
	pl, err := mcpLister.List(labels.Everything())
	if err != nil {
		return nil, nil, nil, err
	}

	var pools []*mcfgv1.MachineConfigPool
	for _, p := range pl {
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid label selector: %w", err)
		}

		// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(node.Labels)) {
			continue
		}

		pools = append(pools, p)
	}

	if len(pools) == 0 {
		// This is not an error, as there might be nodes in cluster that are not managed by machineconfigpool.
		return nil, nil, nil, nil
	}

	var master, worker *mcfgv1.MachineConfigPool
	var custom []*mcfgv1.MachineConfigPool
	for _, pool := range pools {
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

// IsCoreOSNode checks whether the pretty name of a node matches any of the
// coreos based image names
func IsCoreOSNode(node *corev1.Node) bool {
	validOSImages := []string{osrelease.RHCOS, osrelease.FCOS, osrelease.SCOS}

	for _, img := range validOSImages {
		if strings.Contains(node.Status.NodeInfo.OSImage, img) {
			return true
		}
	}
	return false
}
