package rollout

import (
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
)

func isNodeImageEqualToMachineOSConfig(node corev1.Node, mosc *mcfgv1alpha1.MachineOSConfig) bool {
	desired := node.Annotations[daemonconsts.DesiredImageAnnotationKey]
	current := node.Annotations[daemonconsts.CurrentImageAnnotationKey]
	mcdState := node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey]

	return desired == current && mcdState == daemonconsts.MachineConfigDaemonStateDone && current == mosc.Status.CurrentImagePullspec
}
