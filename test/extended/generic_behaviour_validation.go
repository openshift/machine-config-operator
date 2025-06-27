package extended

import (
	"fmt"

	o "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

type Checker interface {
	Check(checkedNodes ...Node)
}

type CommandOutputChecker struct {
	Command  []string
	Matcher  types.GomegaMatcher
	ErrorMsg string
	Desc     string
}

func (cOutChecker CommandOutputChecker) Check(checkedNodes ...Node) {
	msg := "Executing verification commands"
	if cOutChecker.Desc != "" {
		msg = cOutChecker.Desc
	}
	exutil.By(msg)
	o.Expect(checkedNodes).NotTo(o.BeEmpty(), "Refuse to check an empty list of nodes")

	for _, node := range checkedNodes {
		logger.Infof("In node %s. Executing command %s", node.GetName(), cOutChecker.Command)
		o.Expect(
			node.DebugNodeWithChroot(cOutChecker.Command...),
		).To(cOutChecker.Matcher,
			"Command %s validation failed in node %s: %s", cOutChecker.Command, node.GetName(), cOutChecker.ErrorMsg)
	}
	logger.Infof("OK!\n")

}

type RemoteFileChecker struct {
	FileFullPath string
	Matcher      types.GomegaMatcher
	ErrorMsg     string
	Desc         string
}

func (rfc RemoteFileChecker) Check(checkedNodes ...Node) {
	msg := fmt.Sprintf("Checking file: %s", rfc.FileFullPath)
	if rfc.Desc != "" {
		msg = rfc.Desc
	}
	exutil.By(msg)
	o.Expect(checkedNodes).NotTo(o.BeEmpty(), "Refuse to check an empty list of nodes")

	for _, node := range checkedNodes {
		rf := NewRemoteFile(node, rfc.FileFullPath)
		logger.Infof("Checking remote file %s", rf)
		o.Expect(rf).To(rfc.Matcher,
			"Validation of %s failed: %", rf, rfc.ErrorMsg)
	}
	logger.Infof("OK!\n")
}
