package common

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// This is intended to provide a singular way to interrogate node objects to
// determine if they're in a specific state. A secondary goal is to provide a
// singular way to mutate node objects for the purposes of updating their
// current configurations.
//
// The eventual goal is to replace all of the node status functions in
// status.go with this code, then repackage this so that it can be used by any
// portion of the MCO which needs to interrogate or mutate node state.
type LayeredNodeState struct {
	node *corev1.Node
}

func NewLayeredNodeState(n *corev1.Node) *LayeredNodeState {
	return &LayeredNodeState{node: n}
}

// Checks if the node is done "working." For a node in both layered and non-layered MCPs, the
// node's state annotation must be "Done" and it must be targeting the correct MachineConfig. For
// `layered` MCPs, the node's desired image annotation must match the image defined in the
// MachineOsConfig and the desired MachineConfig must match the config version defined in the
// MachineOSBuild. For non-layered MCPs, the node must not have a desired image annotation value.
func (l *LayeredNodeState) IsDone(mcp *mcfgv1.MachineConfigPool, layered bool, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	if layered {
		return l.IsNodeDone() && l.IsDesiredMachineConfigEqualToPool(mcp) && l.IsDesiredEqualToBuild(mosc, mosb)
	}
	return l.IsNodeDone() && l.IsDesiredMachineConfigEqualToPool(mcp) && !l.IsDesiredImageAnnotationPresentOnNode()
}

// The original behavior of getUnavailableMachines is: getUnavailableMachines
// returns the set of nodes which are either marked unscheduleable, or have a
// MCD actively working. If the MCD is actively working (or hasn't started)
// then the node *may* go unschedulable in the future, so we don't want to
// potentially start another node update exceeding our maxUnavailable. Somewhat
// the opposite of getReadyNodes().
//
// This augments this check by determining if the current and desired image
// annotations are transitioning to / from a layered state or transitioning
// from one image to another.
func (l *LayeredNodeState) IsUnavailableForUpdate() bool {
	// Unready nodes are unavailable
	if !l.IsNodeReady() {
		return true
	}
	// Nodes that are still working towards an MC/image is not done and is unavailable
	if l.IsNodeDone() {
		return false
	}
	// Now we know the node isn't ready - the current config must not
	// equal target.  We want to further filter down on the MCD state.
	// If a MCD is in a terminal (failing) state then we can safely retarget it.
	// to a different config.  Or to say it another way, a node is unavailable
	// if the MCD is working, or hasn't started work but the configs differ.
	return !l.IsNodeMCDFailing()
}

// Compares the images specified by the MachineOSConfig to the one specified by the
// node's desired image annotation and compares the MachineConfig specified by the
// MachineOSBuild to the one specified by the node's desired MachineConfig annotation.
func (l *LayeredNodeState) IsDesiredEqualToBuild(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	return (mosc != nil && l.isDesiredImageEqualToMachineOSConfig(mosc)) && (mosb != nil && l.isDesiredMachineConfigEqualToBuild(mosb))
}

// Compares the desired image annotation on the node against the CurrentImagePullSpec
// specified by the MachineOSConfig object.
func (l *LayeredNodeState) isDesiredImageEqualToMachineOSConfig(mosc *mcfgv1.MachineOSConfig) bool {
	return l.isImageAnnotationEqualToMachineOSConfig(daemonconsts.DesiredImageAnnotationKey, mosc)
}

// Compares the MachineConfig specified by the MachineConfigPool to the MachineConfig
// specified by the node's desired MachineConfig annotation.
func (l *LayeredNodeState) isDesiredMachineConfigEqualToBuild(mosb *mcfgv1.MachineOSBuild) bool {
	return l.node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] == mosb.Spec.MachineConfig.Name

}

// Compares the MachineConfig specified by the MachineConfigPool to the one
// specified by the node's desired MachineConfig annotation.
func (l *LayeredNodeState) IsCurrentMachineConfigEqualToPool(mcp *mcfgv1.MachineConfigPool) bool {
	return l.node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] == mcp.Spec.Configuration.Name
}

// Compares the MachineConfig specified by the MachineConfigPool to the one
// specified by the node's desired MachineConfig annotation.
func (l *LayeredNodeState) IsDesiredMachineConfigEqualToPool(mcp *mcfgv1.MachineConfigPool) bool {
	return l.node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] == mcp.Spec.Configuration.Name
}

// Checks if both the current and desired image annotation are present on the node.
func (l *LayeredNodeState) AreImageAnnotationsPresentOnNode() bool {
	return l.IsDesiredImageAnnotationPresentOnNode() && l.IsCurrentImageAnnotationPresentOnNode()
}

// Checks if the desired image annotation is present on the node.
func (l *LayeredNodeState) IsDesiredImageAnnotationPresentOnNode() bool {
	return metav1.HasAnnotation(l.Node().ObjectMeta, daemonconsts.DesiredImageAnnotationKey)
}

// Checks if the current image annotation is present on the node.
func (l *LayeredNodeState) IsCurrentImageAnnotationPresentOnNode() bool {
	return metav1.HasAnnotation(l.Node().ObjectMeta, daemonconsts.CurrentImageAnnotationKey)
}

// Compares the CurrentImagePullSpec specified by the MachineOSConfig object against the
// image specified by the annotation passed in.
func (l *LayeredNodeState) isImageAnnotationEqualToMachineOSConfig(anno string, mosc *mcfgv1.MachineOSConfig) bool {
	moscs := NewMachineOSConfigState(mosc)

	val, ok := l.node.Annotations[anno]

	// If a layered node does not have an image annotation and has a valid MOSC, then the image is being built
	if val == "" || !ok {
		return false
	}

	if moscs.HasOSImage() {
		// If the MOSC image has an image, the image annotation on the node can be directly compared.
		return moscs.GetOSImage() == val
	}

	// If the MOSC does not have an image, but the node has an older image annotation, the image is still likely
	// being built.
	return false
}

// Sets the desired MachineConfig annotations from the MachineConfigPool.
// Deletes any desired image annotations if they exist.
//
// Note: This will create a deep copy of the node object first to avoid
// mutating any underlying caches.
func (l *LayeredNodeState) SetDesiredStateFromPool(mcp *mcfgv1.MachineConfigPool) {
	node := l.Node()

	metav1.SetMetaDataAnnotation(&node.ObjectMeta, daemonconsts.DesiredMachineConfigAnnotationKey, mcp.Spec.Configuration.Name)
	delete(node.Annotations, daemonconsts.DesiredImageAnnotationKey)

	l.node = node
}

// Sets the desired MachineConfig & image annotations from the MachineOSBuild and
// MachineOSConfig objects.
//
// Note: This will create a deep copy of the node object first to avoid
// mutating any underlying caches.

func (l *LayeredNodeState) SetDesiredStateFromMachineOSConfig(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) {
	node := l.Node()

	metav1.SetMetaDataAnnotation(&node.ObjectMeta, daemonconsts.DesiredMachineConfigAnnotationKey, mosb.Spec.MachineConfig.Name)

	moscs := NewMachineOSConfigState(mosc)
	if moscs.HasOSImage() {
		metav1.SetMetaDataAnnotation(&node.ObjectMeta, daemonconsts.DesiredImageAnnotationKey, moscs.GetOSImage())
	} else {
		delete(node.Annotations, daemonconsts.DesiredImageAnnotationKey)
	}

	l.node = node
}

// Returns a deep copy of the underlying node object.
func (l *LayeredNodeState) Node() *corev1.Node {
	return l.node.DeepCopy()
}

// isNodeDone returns true if the current == desired and the MCD has marked done.
func (l *LayeredNodeState) IsNodeDone() bool {
	if l.node.Annotations == nil {
		return false
	}

	if !l.isNodeConfigDone() {
		return false
	}

	if !l.isNodeImageDone() {
		return false
	}

	if !l.isNodeMCDState(daemonconsts.MachineConfigDaemonStateDone) {
		return false
	}

	return true
}

// Determines if a node's configuration is done based upon the presence and
// equality of the current / desired config annotations.
func (l *LayeredNodeState) isNodeConfigDone() bool {
	cconfig, ok := l.node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	if !ok || cconfig == "" {
		return false
	}

	dconfig, ok := l.node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
	if !ok || dconfig == "" {
		return false
	}

	return cconfig == dconfig
}

// Determines if a node's image is done based upon the presence of the current
// / desired image annotations. Note: Unlike the above function, if both
// annotations are missing, we return "True" because we do not want to take
// these annotations into consideration. Only when one (or both) of these
// annotations is present should we take them into consideration.
// them into consideration.
func (l *LayeredNodeState) isNodeImageDone() bool {
	desired, desiredOK := l.node.Annotations[daemonconsts.DesiredImageAnnotationKey]
	current, currentOK := l.node.Annotations[daemonconsts.CurrentImageAnnotationKey]

	// If neither annotation exists, we are "done" because there are no image
	// annotations to consider. This means that the  node is currently not in
	// layered mode.
	if !desiredOK && !currentOK {
		return true
	}

	// If the desired annotation is empty, we are not "done" yet. This would happen
	// if the node was rolling back to non layered mode.
	if desired == "" {
		return false
	}

	// If the current annotation is empty, we are not "done" yet. This would happen
	// is node is updating an image for the first time.
	if current == "" {
		return false
	}

	// If the current image equals the desired image and neither are empty, we are done.
	return desired == current
}

// isNodeMCDState checks the MCD state against the state parameter
func (l *LayeredNodeState) isNodeMCDState(state string) bool {
	dstate, ok := l.node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey]
	if !ok || dstate == "" {
		return false
	}

	return dstate == state
}

// CheckNodeReady() returns any errors that may prevent the node
// from being scheduled for workloads.
func (l *LayeredNodeState) CheckNodeReady() error {
	for i := range l.node.Status.Conditions {
		cond := &l.node.Status.Conditions[i]
		// We consider the node for scheduling only when its:
		// - NodeReady condition status is ConditionTrue,
		// - NodeDiskPressure condition status is ConditionFalse,
		// - NodeNetworkUnavailable condition status is ConditionFalse.
		if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
			return fmt.Errorf("node %s is reporting NotReady=%v", l.node.Name, cond.Status)
		}
		if cond.Type == corev1.NodeDiskPressure && cond.Status != corev1.ConditionFalse {
			return fmt.Errorf("node %s is reporting OutOfDisk=%v", l.node.Name, cond.Status)
		}
		if cond.Type == corev1.NodeNetworkUnavailable && cond.Status != corev1.ConditionFalse {
			return fmt.Errorf("node %s is reporting NetworkUnavailable=%v", l.node.Name, cond.Status)
		}
	}
	// Ignore nodes that are marked unschedulable
	if l.node.Spec.Unschedulable {
		return fmt.Errorf("node %s is reporting Unschedulable", l.node.Name)
	}
	return nil
}

// IsNodeReady() checks if the node is ready to be scheduled for workloads.
func (l *LayeredNodeState) IsNodeReady() bool {
	return l.CheckNodeReady() == nil
}

// isNodeMCDFailing checks if the MCD has unsuccessfully applied an update
func (l *LayeredNodeState) IsNodeMCDFailing() bool {
	if l.node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] == l.node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] {
		return false
	}
	return l.isNodeMCDState(daemonconsts.MachineConfigDaemonStateDegraded) ||
		l.isNodeMCDState(daemonconsts.MachineConfigDaemonStateUnreconcilable)
}

// CheckNodeCandidacyForUpdate checks if the node is a candidate for an update
func (l *LayeredNodeState) CheckNodeCandidacyForUpdate(layered bool, pool *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	if layered {
		// If the node's desired annotations match the ones described by the MachineOSConfig
		// and MachineOSBuild objects, then the node is not a candidate.
		if l.IsDesiredEqualToBuild(mosc, mosb) {
			klog.V(4).Infof("Pool %s: layered node %s: no update is needed", pool.Name, l.node.Name)
			return false
		}
		return true
	}
	// In non layered mode, the node is not a candidate if its desired config
	// matches the one described by its pool in most cases.
	if l.IsDesiredMachineConfigEqualToPool(pool) {
		// The only exception is when the desired image annotation is present
		// on a non layered node, which means that this node is being
		// opted out of layering. This would make it a candidate for an update.
		if l.IsDesiredImageAnnotationPresentOnNode() {
			klog.V(4).Infof("Pool %s: non-layered node %s: rollback update is needed", pool.Name, l.node.Name)
			return true
		}
		klog.V(4).Infof("Pool %s: non-layered node %s: no update is needed", pool.Name, l.node.Name)
		return false
	}

	return true
}

// GetDesiredAnnotationsFromMachineOSConfig gets the desired config version and desired image values from the associated MOSC and MOSB
func (l *LayeredNodeState) GetDesiredAnnotationsFromMachineConfigPool(mcp *mcfgv1.MachineConfigPool) (desriedConfig string) {
	return mcp.Spec.Configuration.Name
}

// GetDesiredAnnotationsFromMachineOSConfig gets the desired config version and desired image values from the associated MOSC and MOSB
func (l *LayeredNodeState) GetDesiredAnnotationsFromMachineOSConfig(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) (desriedConfig, desiredImage string) {
	desiredImage = ""
	moscs := NewMachineOSConfigState(mosc)
	if moscs.HasOSImage() {
		desiredImage = moscs.GetOSImage()
	}

	return mosb.Spec.MachineConfig.Name, desiredImage
}

// IsNodeDegraded checks if the node is reporting a MCD state of "Degraded"
func (l *LayeredNodeState) IsNodeDegraded() bool {
	return l.isNodeMCDState(daemonconsts.MachineConfigDaemonStateDegraded)
}

// IsNodeUnreconcilable checks if the node is reporting a MCD state of "Unreconcilable"
func (l *LayeredNodeState) IsNodeUnreconcilable() bool {
	return l.isNodeMCDState(daemonconsts.MachineConfigDaemonStateUnreconcilable)
}
