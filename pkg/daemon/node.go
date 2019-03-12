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

func (dn *Daemon) loadNodeAnnotations() error {
	ccAnnotation, err := getNodeAnnotation(dn.node, constants.CurrentMachineConfigAnnotationKey)
	// we need to load the annotations from the file only for the
	// first run.
	// the initial annotations do no need to be set if the node
	// already has annotations.
	if err == nil && ccAnnotation != "" {
		return nil
	}

	d, err := ioutil.ReadFile(constants.InitialNodeAnnotationsFilePath)
	if err != nil {
		return fmt.Errorf("failed to read initial annotations from %q: %v", constants.InitialNodeAnnotationsFilePath, err)
	}

	var initial map[string]string
	err = json.Unmarshal(d, &initial)
	if err != nil {
		return fmt.Errorf("failed to unmarshal initial annotations: %v", err)
	}

	glog.Infof("Setting initial node config: %s", initial[constants.CurrentMachineConfigAnnotationKey])
	node, err := setNodeAnnotations(dn.kubeClient.CoreV1().Nodes(), dn.node.Name, initial)
	if err != nil {
		return fmt.Errorf("failed to set initial annotations: %v", err)
	}
	dn.node = node

	return nil
}

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(node *core_v1.Node, k string) (string, error) {
	return getNodeAnnotationExt(node, k, false)
}

// getNode queries the kube apiserver to get the node named nodeName
func getNode(client corev1.NodeInterface, nodeName string) (*core_v1.Node, error) {
	var lastErr error
	var n *core_v1.Node
	err := wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
		n, lastErr = client.Get(nodeName, metav1.GetOptions{})
		if lastErr == nil {
			return true, nil
		}
		glog.Warningf("Failed to fetch node %s (%v); retrying...", nodeName, lastErr)
		return false, nil
	})
	if err != nil {
		if err == wait.ErrWaitTimeout {
			return nil, errors.Wrapf(lastErr, "timed out trying to fetch node %s", nodeName)
		}
		return nil, err
	}
	return n, nil
}

// setInitialNode gets the node object by querying the api server when the daemon starts
func (dn *Daemon) setInitialNode(nodeName string) error {
	node, err := getNode(dn.kubeClient.CoreV1().Nodes(), nodeName)
	if err != nil {
		return err
	}
	dn.node = node
	return nil
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
