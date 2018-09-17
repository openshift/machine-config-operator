package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
)

const (
	// InitialNodeAnnotationsFilePath defines the path at which it will find the node annotations it needs to set on the node once it comes up for the first time.
	// The Machine Config Server writes the node annotations to this path.
	InitialNodeAnnotationsFilePath = "/etc/machine-config-daemon/node-annotations.json"
)

// waitUntilUpdate blocks until the desiredConfig annotation doesn't match the
// currentConfig annotation, which indicates that there is an update available
// for the node.
func waitUntilUpdate(client corev1.NodeInterface, node string) error {
	n, err := client.Get(node, metav1.GetOptions{})
	if err != nil {
		return err
	}

	watcher, err := client.Watch(metav1.ListOptions{
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", node).String(),
		ResourceVersion: n.ResourceVersion,
	})
	if err != nil {
		return fmt.Errorf("Failed to watch self node (%q): %v", node, err)
	}

	// make sure the condition isn't already true
	dc, err := getNodeAnnotation(client, node, DesiredMachineConfigAnnotationKey)
	if err != nil {
		return err
	}
	cc, err := getNodeAnnotation(client, node, CurrentMachineConfigAnnotationKey)
	if err != nil || cc == "" {
		return err
	}
	if dc != cc {
		return nil
	}

	// loop over the watch. when a nil event is returned that means the timeout
	// has been reached at which point we restart the watcher.
	// Note: previously we tried to wait forever. However, the wait will eventually
	// timeout and cause the MCD to "restart".
	// See: https://github.com/openshift/machine-config-operator/issues/68
	timeout := time.Duration(MachineConfigDaemonWatcherTimeoutInMinutes) * time.Minute
	for {
		glog.V(2).Infof("Watching endpoint for %s", timeout)
		event, err := watch.Until(timeout, watcher, updateWatcher)
		if err != nil {
			return fmt.Errorf("Failed to watch for update request: %v", err)
		}
		// if we have an event, break the loop and fall into the return statement
		if event != nil {
			break
		}
		glog.V(2).Infof("Watcher hit %s timeout", timeout)
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

func loadNodeAnnotations(client corev1.NodeInterface, node string) error {
	ccAnnotation, err := getNodeAnnotation(client, node, CurrentMachineConfigAnnotationKey)

	// we need to load the annotations from the file only for the
	// first run.
	// the initial annotations do no need to be set if the node
	// already has annotations.
	if err == nil && ccAnnotation != "" {
		return nil
	}

	d, err := ioutil.ReadFile(InitialNodeAnnotationsFilePath)
	if err != nil {
		return fmt.Errorf("Failed to read initial annotations from %q: %v", InitialNodeAnnotationsFilePath, err)
	}

	var initial map[string]string
	err = json.Unmarshal(d, &initial)
	if err != nil {
		return fmt.Errorf("Failed to unmarshal initial annotations: %v", err)
	}

	err = setNodeAnnotations(client, node, initial)
	if err != nil {
		return fmt.Errorf("Failed to set initial annotations: %v", err)
	}
	return nil
}

func getNodeAnnotation(client corev1.NodeInterface, node string, k string) (string, error) {
	n, err := client.Get(node, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	v, ok := n.Annotations[k]
	if !ok {
		return "", fmt.Errorf("%s annotation not found in %s", k, node)
	}

	return v, nil
}

// updateWatcher is the handler for the watch event.
func updateWatcher(event watch.Event) (bool, error) {
	switch event.Type {
	case watch.Modified:
		node := event.Object.(*v1.Node)
		return node.Annotations[DesiredMachineConfigAnnotationKey] != node.Annotations[CurrentMachineConfigAnnotationKey], nil
	}

	return false, nil
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

func setUpdateDone(client corev1.NodeInterface, node string) error {
	dcAnnotation, err := getNodeAnnotation(client, node, DesiredMachineConfigAnnotationKey)
	if err != nil {
		return err
	}

	annos := map[string]string{
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateDone,
		CurrentMachineConfigAnnotationKey:     dcAnnotation,
	}
	return setNodeAnnotations(client, node, annos)
}

func setUpdateWorking(client corev1.NodeInterface, node string) error {
	annos := map[string]string{
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateWorking,
	}
	return setNodeAnnotations(client, node, annos)
}

func setUpdateDegraded(client corev1.NodeInterface, node string) error {
	annos := map[string]string{
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateDegraded,
	}
	return setNodeAnnotations(client, node, annos)
}
