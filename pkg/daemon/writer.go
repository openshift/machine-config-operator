package daemon

import (
	"fmt"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
)

const (
	// machineConfigDaemonSSHAccessAnnotationKey is used to mark a node after it has been accessed via SSH
	machineConfigDaemonSSHAccessAnnotationKey = "machineconfiguration.openshift.io/ssh"
	// MachineConfigDaemonSSHAccessValue is the annotation value applied when ssh access is detected
	machineConfigDaemonSSHAccessValue = "accessed"
)

func (dn *Daemon) setDone(dcAnnotation string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
		constants.CurrentMachineConfigAnnotationKey:     dcAnnotation,
		// clear out any Degraded/Unreconcilable reason
		constants.MachineConfigDaemonReasonAnnotationKey: "",
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDone, "").SetToCurrentTime()
	_, err := dn.setNodeAnnotations(annos)
	return err
}

func (dn *Daemon) setWorking() error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateWorking,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateWorking, "").SetToCurrentTime()
	_, err := dn.setNodeAnnotations(annos)
	return err
}

func (dn *Daemon) setUnreconcilable(err error) error {
	glog.Errorf("Marking Unreconcilable due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateUnreconcilable,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateUnreconcilable, truncatedErr).SetToCurrentTime()
	_, err = dn.setNodeAnnotations(annos)
	return err
}

func (dn *Daemon) setDegraded(err error) error {
	glog.Errorf("Marking Degraded due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateDegraded,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDegraded, truncatedErr).SetToCurrentTime()
	_, err = dn.setNodeAnnotations(annos)
	return err
}

func (dn *Daemon) setSSHAccessed() error {
	MCDSSHAccessed.Inc()
	annos := map[string]string{
		machineConfigDaemonSSHAccessAnnotationKey: machineConfigDaemonSSHAccessValue,
	}
	_, err := dn.setNodeAnnotationsRetry(annos)
	return err
}

func (dn *Daemon) setNodeAnnotations(m map[string]string) (*corev1.Node, error) {
	return internal.UpdateNode(dn.kubeClient.CoreV1().Nodes(), dn.nodeLister, dn.name, func(node *corev1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
}

func (dn *Daemon) setNodeAnnotationsRetry(m map[string]string) (*corev1.Node, error) {
	return internal.UpdateNodeRetry(dn.kubeClient.CoreV1().Nodes(), dn.nodeLister, dn.name, func(node *corev1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
}
