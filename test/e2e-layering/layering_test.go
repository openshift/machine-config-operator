package e2e_layering_test

import (
	"testing"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/suite"
)

type LayeringSuite struct {
	suite.Suite
}

func (suite *LayeringSuite) SetupSuite() {
	cs := framework.NewClientSet("")

	// don't keep unlabel function or delete pool function since removing a node from a layered pool is...not well defined
	helpers.LabelAllNodesInPool(suite.T(), cs, "worker", "node-role.kubernetes.io/"+ctrlcommon.ExperimentalLayeringPoolName)
	helpers.CreateMCP(suite.T(), cs, ctrlcommon.ExperimentalLayeringPoolName)

	// TODO wait for that to rollout and assert that it rebased
}

func (suite *LayeringSuite) TestNoReboot() {
	// TODO
}

func TestLayeringSuite(t *testing.T) {
	suite.Run(t, new(LayeringSuite))
}
