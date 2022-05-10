package daemon

import (
	"fmt"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
)

const (
	// defaultWriterQueue the number of pending writes to queue
	defaultWriterQueue = 25

	// machineConfigDaemonSSHAccessAnnotationKey is used to mark a node after it has been accessed via SSH
	machineConfigDaemonSSHAccessAnnotationKey = "machineconfiguration.openshift.io/ssh"
	// MachineConfigDaemonSSHAccessValue is the annotation value applied when ssh access is detected
	machineConfigDaemonSSHAccessValue = "accessed"
)

// message wraps a client and responseChannel
type message struct {
	annos           map[string]string
	responseChannel chan error
}

// clusterNodeWriter is a single writer to Kubernetes to prevent race conditions
type clusterNodeWriter struct {
	nodeName string
	writer   chan message
	client   corev1client.NodeInterface
	lister   corev1lister.NodeLister
}

// NodeWriter is the interface to implement a single writer to Kubernetes to prevent race conditions
type NodeWriter interface {
	Run(stop <-chan struct{})
	SetDone(dcAnnotation string) error
	SetWorking() error
	SetUnreconcilable(err error) error
	SetDegraded(err error) error
	SetSSHAccessed() error
	SetAnnotations(annos map[string]string) error
}

// newNodeWriter Create a new NodeWriter
func newNodeWriter(nodeName string, client corev1client.NodeInterface, lister corev1lister.NodeLister) NodeWriter {
	return &clusterNodeWriter{
		nodeName: nodeName,
		client:   client,
		lister:   lister,
		writer:   make(chan message, defaultWriterQueue),
	}
}

// Run reads from the writer channel and sets the node annotation. It will
// return if the stop channel is closed. Intended to be run via a goroutine.
func (nw *clusterNodeWriter) Run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case msg := <-nw.writer:
			_, err := setNodeAnnotations(nw.client, nw.lister, nw.nodeName, msg.annos)
			msg.responseChannel <- err
		}
	}
}

// SetDone sets the state to Done.
func (nw *clusterNodeWriter) SetDone(dcAnnotation string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
		constants.CurrentMachineConfigAnnotationKey:     dcAnnotation,
		// clear out any Degraded/Unreconcilable reason
		constants.MachineConfigDaemonReasonAnnotationKey: "",
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDone, "").SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetWorking sets the state to Working.
func (nw *clusterNodeWriter) SetWorking() error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateWorking,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateWorking, "").SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetUnreconcilable sets the state to Unreconcilable.
func (nw *clusterNodeWriter) SetUnreconcilable(err error) error {
	glog.Errorf("Marking Unreconcilable due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateUnreconcilable,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateUnreconcilable, truncatedErr).SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	clientErr := <-respChan
	if clientErr != nil {
		glog.Errorf("Error setting Unreconcilable annotation for node %s: %v", nw.nodeName, clientErr)
	}
	return clientErr
}

// SetDegraded logs the error and sets the state to Degraded.
// Returns an error if it couldn't set the annotation.
func (nw *clusterNodeWriter) SetDegraded(err error) error {
	glog.Errorf("Marking Degraded due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateDegraded,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDegraded, truncatedErr).SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	clientErr := <-respChan
	if clientErr != nil {
		glog.Errorf("Error setting Degraded annotation for node %s: %v", nw.nodeName, clientErr)
	}
	return clientErr
}

// SetSSHAccessed sets the ssh annotation to accessed
func (nw *clusterNodeWriter) SetSSHAccessed() error {
	MCDSSHAccessed.Inc()
	annos := map[string]string{
		machineConfigDaemonSSHAccessAnnotationKey: machineConfigDaemonSSHAccessValue,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

func (nw *clusterNodeWriter) SetAnnotations(annos map[string]string) error {
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	err := <-respChan
	return err
}

func setNodeAnnotations(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, m map[string]string) (*corev1.Node, error) {
	node, err := internal.UpdateNodeRetry(client, lister, nodeName, func(node *corev1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
	return node, err
}
