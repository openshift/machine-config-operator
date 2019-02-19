package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/pkg/errors"
	core_v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

func loadNodeAnnotations(client corev1.NodeInterface, nodeName string) error {
	node, err := GetNode(client, nodeName)
	if err != nil {
		return err
	}

	ccAnnotation, err := getNodeAnnotation(node, constants.CurrentMachineConfigAnnotationKey)

	// we need to load the annotations from the file only for the
	// first run.
	// the initial annotations do no need to be set if the node
	// already has annotations.
	if err == nil && ccAnnotation != "" {
		return nil
	}

	d, err := ioutil.ReadFile(constants.InitialNodeAnnotationsFilePath)
	if err != nil {
		return fmt.Errorf("Failed to read initial annotations from %q: %v", constants.InitialNodeAnnotationsFilePath, err)
	}

	var initial map[string]string
	err = json.Unmarshal(d, &initial)
	if err != nil {
		return fmt.Errorf("Failed to unmarshal initial annotations: %v", err)
	}

	glog.Infof("Setting initial node config: %s", initial[constants.CurrentMachineConfigAnnotationKey])
	err = setNodeAnnotations(client, nodeName, initial)
	if err != nil {
		return fmt.Errorf("Failed to set initial annotations: %v", err)
	}
	return nil
}

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(node *core_v1.Node, k string) (string, error) {
	return getNodeAnnotationExt(node, k, false)
}

// GetNode gets the node object.
func GetNode(client corev1.NodeInterface, node string) (*core_v1.Node, error) {
	var lastErr error
	var n *core_v1.Node
	err := wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
		n, lastErr = client.Get(node, metav1.GetOptions{})
		if lastErr == nil {
			return true, nil
		}
		glog.Warningf("Failed to fetch node %s (%v); retrying...", node, lastErr)
		return false, nil
	})
	if err != nil {
		if err == wait.ErrWaitTimeout {
			return nil, errors.Wrapf(lastErr, "Timed out trying to fetch node %s", node)
		}
		return nil, err
	}
	return n, nil
}

// getNodeAnnotationExt is like getNodeAnnotation, but allows one to customize ENOENT handling
func getNodeAnnotationExt(node *core_v1.Node, k string, allowNoent bool) (string, error) {
	v, ok := node.Annotations[k]
	if !ok {
		if !allowNoent {
			return "", fmt.Errorf("%s annotation not found in %s", k, node)
		}
		return "", nil
	}

	return v, nil
}
