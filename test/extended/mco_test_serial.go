package extended

import (
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/conformance/serial][Serial] Test serial", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("test-serial", exutil.KubeConfigPath())
	)

	g.It("test-1", func() {
		exutil.By("Running test...")
		mcp := GetCompactCompatiblePool(oc.AsAdmin())
		o.Expect(mcp).NotTo(o.BeNil())
		logger.Infof("OK!\n")
	})
})
