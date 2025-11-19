// TODO (MCO-1960): Deduplicate these functions with the helpers defined in /extended-priv/node.go.
package extended

import (
	"context"
	"fmt"
	"time"

	o "github.com/onsi/gomega"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Returns the set of ready nodes in the cluster
func getReadyNodes(oc *exutil.CLI) (sets.Set[string], error) {
	nodeList := extpriv.NewNodeList(oc.AsAdmin())
	nodes, err := nodeList.GetAllReady()
	if err != nil {
		return nil, err
	}

	nodeSet := sets.New[string]()
	for _, node := range nodes {
		nodeSet.Insert(node.GetName())
	}
	return nodeSet, nil
}

// `GetNodesByRole` gets all nodes labeled with the desired role
func GetNodesByRole(oc *exutil.CLI, role string) ([]corev1.Node, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{fmt.Sprintf("node-role.kubernetes.io/%s", role): ""}).String(),
	}
	nodes, err := oc.AsAdmin().KubeClient().CoreV1().Nodes().List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	return nodes.Items, nil
}

// `WaitForNodeCurrentConfig` waits up to 5 minutes for a input node to have a current
// config equal to the `config` parameter
func WaitForNodeCurrentConfig(oc *exutil.CLI, nodeName, config string) {
	o.Eventually(func() bool {
		node, nodeErr := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if nodeErr != nil {
			logger.Infof("Failed to get node '%v', error :%v", nodeName, nodeErr)
			return false
		}

		// Check if the node's current config matches the input config version
		nodeCurrentConfig := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
		if nodeCurrentConfig == config {
			logger.Infof("Node '%v' has successfully updated and has a current config version of '%v'.", nodeName, nodeCurrentConfig)
			return true
		}
		logger.Infof("Node '%v' has a current config version of '%v'. Waiting for the node's current config version to be '%v'.", nodeName, nodeCurrentConfig, config)
		return false
	}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Timed out waiting for node '%v' to have a current config version of '%v'.", nodeName, config)
}
