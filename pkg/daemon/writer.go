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
	client          corev1client.NodeInterface
	lister          corev1lister.NodeLister
	node            string
	annos           map[string]string
	responseChannel chan error
}

// clusterNodeWriter is a single writer to Kubernetes to prevent race conditions
type clusterNodeWriter struct {
	writer chan message
}

// NodeWriter is the interface to implement a single writer to Kubernetes to prevent race conditions
type NodeWriter interface {
	Run(stop <-chan struct{})
	SetDone(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string, dcAnnotation string) error
	SetWorking(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error
	SetUnreconcilable(err error, client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error
	SetDegraded(err error, client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error
	SetSSHAccessed(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error
	SetEtcdLeader(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string, leader bool) error
}

// newNodeWriter Create a new NodeWriter
func newNodeWriter() NodeWriter {
	return &clusterNodeWriter{
		writer: make(chan message, defaultWriterQueue),
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
			_, err := setNodeAnnotations(msg.client, msg.lister, msg.node, msg.annos)
			msg.responseChannel <- err
		}
	}
}

// SetDone sets the state to Done.
func (nw *clusterNodeWriter) SetDone(client corev1client.NodeInterface, lister corev1lister.NodeLister, node, dcAnnotation string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
		constants.CurrentMachineConfigAnnotationKey:     dcAnnotation,
		// clear out any Degraded/Unreconcilable reason
		constants.MachineConfigDaemonReasonAnnotationKey: "",
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDone, "").SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		lister:          lister,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetWorking sets the state to Working.
func (nw *clusterNodeWriter) SetWorking(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateWorking,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateWorking, "").SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		lister:          lister,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetUnreconcilable sets the state to Unreconcilable.
func (nw *clusterNodeWriter) SetUnreconcilable(err error, client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
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
		client:          client,
		lister:          lister,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	clientErr := <-respChan
	if clientErr != nil {
		glog.Errorf("Error setting Unreconcilable annotation for node %s: %v", node, clientErr)
	}
	return clientErr
}

// SetDegraded logs the error and sets the state to Degraded.
// Returns an error if it couldn't set the annotation.
func (nw *clusterNodeWriter) SetDegraded(err error, client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
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
		client:          client,
		lister:          lister,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	clientErr := <-respChan
	if clientErr != nil {
		glog.Errorf("Error setting Degraded annotation for node %s: %v", node, clientErr)
	}
	return clientErr
}

// SetSSHAccessed sets the ssh annotation to accessed
func (nw *clusterNodeWriter) SetSSHAccessed(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
	MCDSSHAccessed.Inc()
	annos := map[string]string{
		machineConfigDaemonSSHAccessAnnotationKey: machineConfigDaemonSSHAccessValue,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		lister:          lister,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetEtcdLeader updates the node's etcd leader annotation
func (nw *clusterNodeWriter) SetEtcdLeader(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string, leader bool) error {
	val := "0"
	if leader {
		val = "1"
	}
	annos := map[string]string{
		constants.UpdateDisruptionScoreAnnotationKey: val,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		lister:          lister,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

func setNodeAnnotations(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, m map[string]string) (*corev1.Node, error) {
	node, err := internal.UpdateNodeRetry(client, lister, nodeName, func(node *corev1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
	return node, err
}
