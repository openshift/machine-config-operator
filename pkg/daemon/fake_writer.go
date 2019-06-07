package daemon

import (
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
)

// fakeNodeWriter is a fake single writer to Kubernetes to prevent race conditions
type fakeNodeWriter struct {
}

// newFakeNodeWriter Create a new NodeWriter
func newFakeNodeWriter() NodeWriter {
	return &fakeNodeWriter{}
}

// Run reads from the writer channel and sets the node annotation. It will
// return if the stop channel is closed. Intended to be run via a goroutine.
func (nw *fakeNodeWriter) Run(stop <-chan struct{}) {
}

// SetDone sets the state to Done.
func (nw *fakeNodeWriter) SetDone(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string, dcAnnotation string) error {
	return nil
}

// SetWorking sets the state to Working.
func (nw *fakeNodeWriter) SetWorking(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
	return nil
}

// SetUnreconcilable sets the state to Unreconcilable.
func (nw *fakeNodeWriter) SetUnreconcilable(err error, client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
	return nil
}

// SetDegraded logs the error and sets the state to Degraded.
// Returns an error if it couldn't set the annotation.
func (nw *fakeNodeWriter) SetDegraded(err error, client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
	return nil
}

// SetSSHAccessed sets the ssh annotation to accessed
func (nw *fakeNodeWriter) SetSSHAccessed(client corev1client.NodeInterface, lister corev1lister.NodeLister, node string) error {
	return nil
}
