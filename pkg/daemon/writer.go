package daemon

import (
	"fmt"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
)

const (
	// defaultWriterQueue the number of pending writes to queue
	defaultWriterQueue = 25
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
			msg.responseChannel <- setNodeAnnotations(msg.client, msg.node, msg.annos)
		}
	}
}

// SetUpdateDone Sets the state to UpdateDone.
func (nw *NodeWriter) SetUpdateDone(client corev1.NodeInterface, node string, dcAnnotation string) error {
	annos := map[string]string{
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateDone,
		CurrentMachineConfigAnnotationKey:     dcAnnotation,
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
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateWorking,
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
	glog.Errorf("Marking degraded due to: %v", err)
	annos := map[string]string{
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateDegraded,
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
	degraded_err := nw.SetUpdateDegraded(err, client, node)
	if degraded_err != nil {
		glog.Errorf("Error while setting degraded: %v", degraded_err)
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
                MachineConfigDaemonSSHAccessAnnotationKey: MachineConfigDaemonSSHAccessValue,
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
func updateNodeRetry(client corev1.NodeInterface, node string, f func(*v1.Node)) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		n, getErr := client.Get(node, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}

		// Call the node modifier.
		f(n)

		_, err := client.Update(n)
		return err
	})
	if err != nil {
		// may be conflict if max retries were hit
		return fmt.Errorf("Unable to update node %q: %v", node, err)
	}

	return nil
}

// setConfig sets the given annotation key, value pair.
func setNodeAnnotations(client corev1.NodeInterface, node string, m map[string]string) error {
	return updateNodeRetry(client, node, func(node *v1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
}
