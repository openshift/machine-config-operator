package e2e_test

import (
	"log"
	"os"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
)

var nodeLeaser *framework.NodeLeaser

func TestMain(m *testing.M) {
	nodeLeaser = getNodeLeaser()
	os.Exit(m.Run())
}

func getNodeLeaser() *framework.NodeLeaser {
	cs := framework.NewClientSet("")
	nl, err := framework.NewNodeLeaser(cs.CoreV1Interface, "worker")
	if err != nil {
		log.Fatalln(err)
	}

	return nl
}
