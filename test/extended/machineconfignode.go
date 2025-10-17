package extended

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
	"github.com/openshift/machine-config-operator/test/framework"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MachineConfigNode resource type declaration
type MachineConfigNode struct {
	Resource
}

// NewMachineConfigNode constructor to get MCN resource
func NewMachineConfigNode(oc *exutil.CLI, node string) *MachineConfigNode {
	return &MachineConfigNode{Resource: *NewResource(oc, "machineconfignode", node)}
}

// `ValidateMCNForNode` validates the MCN of a provided node by checking the following:
//   - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
//   - Check that `mcn.Name` matches the node name
//   - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
//   - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
//   - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
//   - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image
//   - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image
//   - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image
func ValidateMCNForNode(oc *exutil.CLI, clientSet *framework.ClientSet, nodeName, poolName string) error {
	// Get updated node
	node, nodeErr := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if nodeErr != nil {
		logger.Errorf("Could not get node `%v`", nodeName)
		return nodeErr
	}

	// Get node's desired and current config versions and images
	nodeCurrentConfig := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
	nodeDesiredConfig := node.Annotations[constants.DesiredMachineConfigAnnotationKey]
	nodeCurrentImage := mcfgv1.ImageDigestFormat(node.Annotations[constants.CurrentImageAnnotationKey])
	nodeDesiredImage := mcfgv1.ImageDigestFormat(node.Annotations[constants.DesiredImageAnnotationKey])

	// Get node's MCN
	logger.Infof("Getting MCN for node `%v`.", node.Name)
	mcn, mcnErr := clientSet.GetMcfgclient().MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	if mcnErr != nil {
		logger.Errorf("Could not get MCN for node `%v`", node.Name)
		return mcnErr
	}

	// Check MCN pool name value for default MCPs
	logger.Infof("Checking MCN pool name for node `%v` matches pool association `%v`.", node.Name, poolName)
	if mcn.Spec.Pool.Name != poolName {
		logger.Errorf("MCN pool name `%v` does not match node MCP association `%v`.", mcn.Spec.Pool.Name, poolName)
		return fmt.Errorf("MCN pool name does not match expected node MCP association.")
	}

	// Check MCN name matches node name
	logger.Infof("Checking MCN name matches node name `%v`.", node.Name)
	if mcn.Name != node.Name {
		logger.Errorf("MCN name `%v` does not match node name `%v`.", mcn.Name, node.Name)
		return fmt.Errorf("MCN name does not match node name.")
	}

	// Check desired config version in MCN spec matches desired config on node
	logger.Infof("Checking node `%v` desired config version `%v` matches desired config version in MCN spec.", node.Name, nodeDesiredConfig)
	if mcn.Spec.ConfigVersion.Desired != nodeDesiredConfig {
		logger.Errorf("MCN spec desired config version `%v` does not match node desired config version `%v`.", mcn.Spec.ConfigVersion.Desired, nodeDesiredConfig)
		return fmt.Errorf("MCN spec desired config version does not match node desired config version")
	}

	// Check current config version in MCN status matches current config on node
	logger.Infof("Checking node `%v` current config version `%v` matches current version in MCN status.", node.Name, nodeCurrentConfig)
	if mcn.Status.ConfigVersion.Current != nodeCurrentConfig {
		logger.Infof("MCN status current config version `%v` does not match node current config version `%v`.", mcn.Status.ConfigVersion.Current, nodeCurrentConfig)
		return fmt.Errorf("MCN status current config version does not match node current config version")
	}

	// Check desired config version in MCN status matches desired config on node
	logger.Infof("Checking node `%v` desired config version `%v` matches desired version in MCN status.", node.Name, nodeDesiredConfig)
	if mcn.Status.ConfigVersion.Desired != nodeDesiredConfig {
		logger.Infof("MCN status desired config version `%v` does not match node desired config version `%v`.", mcn.Status.ConfigVersion.Desired, nodeDesiredConfig)
		return fmt.Errorf("MCN status desired config version does not match node desired config version")
	}

	// Check desired image in MCN spec matches desired image on node
	logger.Infof("Checking node `%v` desired image `%v` matches desired image in MCN spec.", node.Name, nodeDesiredImage)
	if mcn.Spec.ConfigImage.DesiredImage != nodeDesiredImage {
		logger.Errorf("MCN spec desired image `%v` does not match node desired image `%v`.", mcn.Spec.ConfigImage.DesiredImage, nodeDesiredImage)
		return fmt.Errorf("MCN spec desired image does not match node desired image")
	}

	// Check current image in MCN status matches current image on node
	logger.Infof("Checking node `%v` current image `%v` matches current image in MCN status.", node.Name, nodeCurrentImage)
	if mcn.Status.ConfigImage.CurrentImage != nodeCurrentImage {
		logger.Infof("MCN status current image `%v` does not match node current image `%v`.", mcn.Status.ConfigImage.CurrentImage, nodeCurrentImage)
		return fmt.Errorf("MCN status current image does not match node current image")
	}

	// Check desired image in MCN status matches desired image on node
	logger.Infof("Checking node `%v` desired image `%v` matches desired image in MCN status.", node.Name, nodeDesiredImage)
	if mcn.Status.ConfigImage.DesiredImage != nodeDesiredImage {
		logger.Infof("MCN status desired image `%v` does not match node desired image `%v`.", mcn.Status.ConfigImage.DesiredImage, nodeDesiredImage)
		return fmt.Errorf("MCN status desired image does not match node desired image")
	}

	return nil
}
