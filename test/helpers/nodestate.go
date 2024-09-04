package helpers

import (
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
)

// TODO: Consider consolidating all of the functions within this file with
// pkg/controller/common/layered_node_state.go. The only reason why I did not
// do this is because a larger refactoring of that package is needed and I did
// not wish to include it at this time.

// Determines whether a node has changed configs.
func hasNodeConfigChanged(node corev1.Node, mcName string) bool {
	current := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
	desired := node.Annotations[constants.DesiredMachineConfigAnnotationKey]

	return current == desired && desired == mcName
}

// Determines whether a node has changed images.
func hasNodeImageChanged(node corev1.Node, image string) bool {
	current := node.Annotations[constants.CurrentImageAnnotationKey]
	desired := node.Annotations[constants.DesiredImageAnnotationKey]

	return current == desired && desired == image
}

// Determines whether the MCD on a given node has reached the "Done" state.
func isMCDDone(node corev1.Node) bool {
	state := node.Annotations[constants.MachineConfigDaemonStateAnnotationKey]
	return state == constants.MachineConfigDaemonStateDone
}

// Determines if a given node is ready.
func isNodeReady(node corev1.Node) bool {
	// If the node is cordoned, it is not ready.
	if node.Spec.Unschedulable {
		return false
	}

	// If the nodes' kubelet is not ready, it is not ready.
	if !isNodeKubeletReady(node) {
		return false
	}

	// If the nodes' MCD is not done, it is not ready.
	if !isMCDDone(node) {
		return false
	}

	return true
}

// Determines if a given node's kubelet is ready.
func isNodeKubeletReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Reason == "KubeletReady" && condition.Status == "True" && condition.Type == "Ready" {
			return true
		}
	}

	return false
}

// Determines if all the nodes in a given NodeList are ready. Returns false if
// any node is not ready.
func isAllNodesReady(nodes *corev1.NodeList) bool {
	for _, node := range nodes.Items {
		if !isNodeReady(node) {
			return false
		}
	}

	return true
}
