package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	core_v1 "k8s.io/api/core/v1"
)

func (dn *Daemon) loadNodeAnnotations(node *core_v1.Node) (*core_v1.Node, error) {
	ccAnnotation, err := getNodeAnnotation(node, constants.CurrentMachineConfigAnnotationKey)
	// we need to load the annotations from the file only for the
	// first run.
	// the initial annotations do no need to be set if the node
	// already has annotations.
	if err == nil && ccAnnotation != "" {
		return node, nil
	}

	glog.Infof("No %s annotation on node %s: %v, in cluster bootstrap, loading initial node annotation from %s", constants.CurrentMachineConfigAnnotationKey, node.Name, node.Annotations, constants.InitialNodeAnnotationsFilePath)

	d, err := ioutil.ReadFile(constants.InitialNodeAnnotationsFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read initial annotations from %q: %v", constants.InitialNodeAnnotationsFilePath, err)
	}

	var initial map[string]string
	if err := json.Unmarshal(d, &initial); err != nil {
		return nil, fmt.Errorf("failed to unmarshal initial annotations: %v", err)
	}

	glog.Infof("Setting initial node config: %s", initial[constants.CurrentMachineConfigAnnotationKey])
	n, err := setNodeAnnotations(dn.kubeClient.CoreV1().Nodes(), dn.nodeLister, node.Name, initial)
	if err != nil {
		return nil, fmt.Errorf("failed to set initial annotations: %v", err)
	}
	return n, nil
}

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(node *core_v1.Node, k string) (string, error) {
	return getNodeAnnotationExt(node, k, false)
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
