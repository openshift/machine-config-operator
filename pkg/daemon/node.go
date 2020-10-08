package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
)

func (dn *Daemon) loadNodeAnnotations(node *corev1.Node) (*corev1.Node, error) {
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
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read initial annotations from %q: %v", constants.InitialNodeAnnotationsFilePath, err)
	}
	if os.IsNotExist(err) {
		// try currentConfig if, for whatever reason we lost annotations? this is super best effort.
		currentOnDisk, err := dn.getCurrentConfigOnDisk()
		if err == nil {
			glog.Infof("Setting initial node config based on current configuration on disk: %s", currentOnDisk.GetName())
			// this state shouldn't really happen, but since we're here, we're just going to set desired == current and state == done,
			// otherwise the MCD will complain it cannot find those annotations
			return setNodeAnnotations(dn.kubeClient.CoreV1().Nodes(), dn.nodeLister, node.Name, map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     currentOnDisk.GetName(),
				constants.DesiredMachineConfigAnnotationKey:     currentOnDisk.GetName(),
				constants.MachineConfigDaemonStateAnnotationKey: "done",
			})
		}
		return nil, err
	}

	var initial map[string]string
	if err := json.Unmarshal(d, &initial); err != nil {
		return nil, fmt.Errorf("failed to unmarshal initial annotations: %v", err)
	}

	glog.Infof("Setting initial node config: %s", initial[constants.CurrentMachineConfigAnnotationKey])
	// this sets both current and desired config, as well as state
	// current should == desired at this point
	n, err := setNodeAnnotations(dn.kubeClient.CoreV1().Nodes(), dn.nodeLister, node.Name, initial)
	if err != nil {
		return nil, fmt.Errorf("failed to set initial annotations: %v", err)
	}
	return n, nil
}

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(node *corev1.Node, k string) (string, error) {
	return getNodeAnnotationExt(node, k, false)
}

// getNodeAnnotationExt is like getNodeAnnotation, but allows one to customize ENOENT handling
func getNodeAnnotationExt(node *corev1.Node, k string, allowNoent bool) (string, error) {
	v, ok := node.Annotations[k]
	if !ok {
		if !allowNoent {
			return "", fmt.Errorf("%s annotation not found on node '%s'", k, node.Name)
		}
		return "", nil
	}

	return v, nil
}
