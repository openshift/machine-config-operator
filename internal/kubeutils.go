package internal

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// UpdateNodeRetry calls f to update a node object in Kubernetes.
// It will attempt to update the node by applying f to it up to DefaultBackoff
// number of times.
// f will be called each time since the node object will likely have changed if
// a retry is necessary.
func UpdateNodeRetry(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, f func(*corev1.Node)) (*corev1.Node, error) {
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
			return fmt.Errorf("failed to create patch for node %q: %w", nodeName, err)
		}

		klog.Infof("UpdateNodeRetry patch %s: %s", nodeName, string(patchBytes))
		node, err = client.Patch(context.TODO(), nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	}); err != nil {
		// may be conflict if max retries were hit
		return nil, fmt.Errorf("unable to update node %q: %w", node, err)
	}
	return node, nil
}
