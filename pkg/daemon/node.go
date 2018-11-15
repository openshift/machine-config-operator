package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	// InitialNodeAnnotationsFilePath defines the path at which it will find the node annotations it needs to set on the node once it comes up for the first time.
	// The Machine Config Server writes the node annotations to this path.
	InitialNodeAnnotationsFilePath = "/etc/machine-config-daemon/node-annotations.json"
)

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

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(client corev1.NodeInterface, node string, k string) (string, error) {
	return getNodeAnnotationExt(client, node, k, false)
}

// getNodeAnnotationExt is like getNodeAnnotation, but allows one to customize ENOENT handling
func getNodeAnnotationExt(client corev1.NodeInterface, node string, k string, allowNoent bool) (string, error) {
	n, err := client.Get(node, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	v, ok := n.Annotations[k]
	if !ok {
		if !allowNoent {
			return "", fmt.Errorf("%s annotation not found in %s", k, node)
		}
		return "", nil
	}

	return v, nil
}
