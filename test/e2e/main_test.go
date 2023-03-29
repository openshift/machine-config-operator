package e2e_test

import (
	"log"
	"os"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var nodeLeaser *framework.NodeLeaser

func TestMain(m *testing.M) {
	nodeLeaser = getNodeLeaser()
	os.Exit(m.Run())
}

func getNodeLeaser() *framework.NodeLeaser {
	cs := framework.NewClientSet("")
	nodeList, err := helpers.GetNodesByRole(cs, "worker")
	if err != nil {
		log.Fatalln(err)
	}

	nodeNames := []string{}

	for _, node := range nodeList {
		nodeNames = append(nodeNames, node.Name)
	}

	return framework.NewNodeLeaser(nodeNames)
}
