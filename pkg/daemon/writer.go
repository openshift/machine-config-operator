package daemon

import (
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/util/retry"
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
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateUnreconcilable,
		constants.MachineConfigDaemonReasonAnnotationKey: err.Error(),
	}
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
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateDegraded,
		constants.MachineConfigDaemonReasonAnnotationKey: err.Error(),
	}
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

// updateNodeRetry calls f to update a node object in Kubernetes.
// It will attempt to update the node by applying f to it up to DefaultBackoff
// number of times.
// f will be called each time since the node object will likely have changed if
// a retry is necessary.
func updateNodeRetry(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, f func(*corev1.Node)) (*corev1.Node, error) {
	var node *corev1.Node
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		n, err := lister.Get(nodeName)
		if err != nil {
			return err
		}
		oldNode, err := json.Marshal(n)
		if err != nil {
			return err
		}

		nodeClone := n.DeepCopy()
		f(nodeClone)

		newNode, err := json.Marshal(nodeClone)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldNode, newNode, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %v", nodeName, err)
		}

		node, err = client.Patch(nodeName, types.StrategicMergePatchType, patchBytes)
		return err
	}); err != nil {
		// may be conflict if max retries were hit
		return nil, fmt.Errorf("unable to update node %q: %v", node, err)
	}
	return node, nil
}

func setNodeAnnotations(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, m map[string]string) (*corev1.Node, error) {
	node, err := updateNodeRetry(client, lister, nodeName, func(node *corev1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
	return node, err
}
