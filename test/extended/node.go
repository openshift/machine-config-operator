package extended

import (
	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"

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
