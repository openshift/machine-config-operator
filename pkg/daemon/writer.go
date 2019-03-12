package daemon

import (
	"fmt"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"k8s.io/api/core/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
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
	client          corev1.NodeInterface
	node            string
	annos           map[string]string
	responseChannel chan error
}

// NodeWriter A single writer to Kubernetes to prevent race conditions
type NodeWriter struct {
	writer chan message
}

// NewNodeWriter Create a new NodeWriter
func NewNodeWriter() *NodeWriter {
	return &NodeWriter{
		writer: make(chan message, defaultWriterQueue),
	}
}

// Run reads from the writer channel and sets the node annotation. It will
// return if the stop channel is closed. Intended to be run via a goroutine.
func (nw *NodeWriter) Run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case msg := <-nw.writer:
			_, err := setNodeAnnotations(msg.client, msg.node, msg.annos)
			msg.responseChannel <- err
		}
	}
}

// SetUpdateDone Sets the state to UpdateDone.
func (nw *NodeWriter) SetUpdateDone(client corev1.NodeInterface, node string, dcAnnotation string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
		constants.CurrentMachineConfigAnnotationKey:     dcAnnotation,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetUpdateWorking Sets the state to UpdateWorking.
func (nw *NodeWriter) SetUpdateWorking(client corev1.NodeInterface, node string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateWorking,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetUpdateDegraded logs the error and sets the state to UpdateDegraded.
// Returns an error if it couldn't set the annotation.
func (nw *NodeWriter) SetUpdateDegraded(err error, client corev1.NodeInterface, node string) error {
	glog.Errorf("marking degraded due to: %v", err)
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDegraded,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
		node:            node,
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetUpdateDegradedIgnoreErr logs the error and sets the state to
// UpdateDegraded. Logs an error if if couldn't set the annotation. Always
// returns the same error that it was passed. This is useful in situations
// where one just wants to return an error to its caller after having set the
// node to degraded due to that error.
func (nw *NodeWriter) SetUpdateDegradedIgnoreErr(err error, client corev1.NodeInterface, node string) error {
	// log error here since the caller won't look at it
	degradedErr := nw.SetUpdateDegraded(err, client, node)
	if degradedErr != nil {
		glog.Errorf("error while setting degraded: %v", degradedErr)
	}
	return err
}

// SetUpdateDegradedMsgIgnoreErr is like SetUpdateDegradedMsgIgnoreErr but
// takes a string and constructs the error object itself.
func (nw *NodeWriter) SetUpdateDegradedMsgIgnoreErr(msg string, client corev1.NodeInterface, node string) error {
	err := fmt.Errorf(msg)
	return nw.SetUpdateDegradedIgnoreErr(err, client, node)
}

// SetSSHAccessed sets the ssh annotation to accessed
func (nw *NodeWriter) SetSSHAccessed(client corev1.NodeInterface, node string) error {
	annos := map[string]string{
		machineConfigDaemonSSHAccessAnnotationKey: machineConfigDaemonSSHAccessValue,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		client:          client,
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
func updateNodeRetry(client corev1.NodeInterface, nodeName string, f func(*v1.Node)) (*v1.Node, error) {
	var node *v1.Node
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		n, getErr := getNode(client, nodeName)
		if getErr != nil {
			return getErr
		}

		// Call the node modifier.
		f(n)

		var err error
		node, err = client.Update(n)
		return err
	})
	if err != nil {
		// may be conflict if max retries were hit
		return nil, fmt.Errorf("unable to update node %q: %v", node, err)
	}

	return node, nil
}

func setNodeAnnotations(client corev1.NodeInterface, nodeName string, m map[string]string) (*v1.Node, error) {
	node, err := updateNodeRetry(client, nodeName, func(node *v1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
	return node, err
}
