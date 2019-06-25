package daemon

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(node *corev1.Node, k string) (string, error) {
	return getNodeAnnotationExt(node, k, false)
}

// getNodeAnnotationExt is like getNodeAnnotation, but allows one to customize ENOENT handling
func getNodeAnnotationExt(node *corev1.Node, k string, allowNoent bool) (string, error) {
	v, ok := node.Annotations[k]
	if !ok {
		if !allowNoent {
			return "", fmt.Errorf("%s annotation not found in %s", k, node)
		}
		return "", nil
	}

	return v, nil
}
