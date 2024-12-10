package daemon

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
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

	klog.Infof("No %s annotation on node %s: %v, in cluster bootstrap, loading initial node annotation from %s", constants.CurrentMachineConfigAnnotationKey, node.Name, node.Annotations, constants.InitialNodeAnnotationsFilePath)

	d, err := os.ReadFile(constants.InitialNodeAnnotationsFilePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read initial annotations from %q: %w", constants.InitialNodeAnnotationsFilePath, err)
	}
	if os.IsNotExist(err) {
		// try currentConfig if, for whatever reason we lost annotations? this is super best effort.
		odc, err := dn.getCurrentConfigOnDisk()
		if err == nil {
			klog.Infof("Setting initial node config based on current configuration on disk: %s", odc.currentConfig.GetName())
			annos := map[string]string{
				constants.CurrentMachineConfigAnnotationKey: odc.currentConfig.GetName(),
			}

			if odc.currentImage != "" {
				annos[constants.CurrentImageAnnotationKey] = odc.currentImage
			}

			return dn.nodeWriter.SetAnnotations(annos)
		}
		return nil, err
	}

	var initial map[string]string
	if err := json.Unmarshal(d, &initial); err != nil {
		return nil, fmt.Errorf("failed to unmarshal initial annotations: %w", err)
	}

	klog.Infof("Setting initial node config: %s", initial[constants.CurrentMachineConfigAnnotationKey])
	node, err = dn.nodeWriter.SetAnnotations(initial)
	if err != nil {
		return nil, fmt.Errorf("failed to set initial annotations: %w", err)
	}
	return node, nil
}

// getNodeAnnotation gets the node annotation, unsurprisingly
func getNodeAnnotation(node *corev1.Node, k string) (string, error) {
	return getNodeAnnotationExt(node, k, false)
}

// getNodeAnnotationExt is like getNodeAnnotation, but allows one to customize ENOENT handling
func getNodeAnnotationExt(node *corev1.Node, k string, allowNoent bool) (string, error) {
	if node == nil || node.Annotations == nil {
		return "", fmt.Errorf("node object is nil")
	}
	v, ok := node.Annotations[k]
	if !ok {
		if !allowNoent {
			return "", fmt.Errorf("%s annotation not found on node '%s'", k, node.Name)
		}
		return "", nil
	}

	return v, nil
}
