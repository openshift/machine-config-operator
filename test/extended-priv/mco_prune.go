package extended

import (
	"fmt"
	"regexp"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:73148][OTP] prune renderedmachineconfigs", func() {
		var (
			mcName                    = "fake-worker-pass-1"
			mcList                    = NewMachineConfigList(oc.AsAdmin())
			wMcp                      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp                      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			NewSortedRenderedMCMaster []*MachineConfig
			matchString               string
		)

		// create machine config
		exutil.By("Create a new MachineConfig")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, "core", "fake-b")}
		defer mc.DeleteWithWait()
		mc.create()
		o.Expect(mMcp.WaitImmediateForUpdatedStatus()).To(o.Succeed(), "Master MCP did not reach Updated status")
		logger.Infof("OK!\n")

		wSpecConf, specErr := wMcp.getConfigNameOfSpec() // get worker MCP name
		o.Expect(specErr).NotTo(o.HaveOccurred())
		mSpecConf, specErr := mMcp.getConfigNameOfSpec() // get master MCP name
		o.Expect(specErr).NotTo(o.HaveOccurred())
		logger.Infof("%s %s \n", wSpecConf, mSpecConf)

		// sort mcList by time and get rendered machine config
		mcList.SortByTimestamp()
		sortedRenderedMCs := mcList.GetMCPRenderedMachineConfigsOrFail()
		logger.Infof(" %s", sortedRenderedMCs)

		sortedMCListMaster := mcList.GetRenderedMachineConfigForMasterOrFail() // to get master rendered machine config
		// 1 To check for `oc adm prune renderedmachineconfigs` cmd
		exutil.By("To run prune cmd to know which rendered machineconfigs would be deleted")
		pruneMCOutput, err := oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config for pool")
		logger.Infof(pruneMCOutput)

		for _, mc := range sortedRenderedMCs {
			matchString := "dry-run deleting rendered MachineConfig "
			if mc.GetName() == wSpecConf || mc.GetName() == mSpecConf {
				matchString = "Skip dry-run deleting rendered MachineConfig "
			}
			o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+mc.GetName()), "The %s is not same as in-use renderedMC in MCP", mc.GetName()) // to check correct rendered MC will be deleted or skipped

			o.Expect(mc.Exists()).To(o.BeTrue(), "The dry run deleted rendered MC is removed but should exist.")
		}
		logger.Infof("OK!\n")

		// 2 To check for `oc adm prune renderedmachineconfigs --count=1 --pool-name master` cmd
		exutil.By("To get the rendered machineconfigs based on count and MCP name")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--count=1", "--pool-name", "master").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config for pool")
		logger.Infof(pruneMCOutput)
		NewSortedRenderedMCMaster = mcList.GetRenderedMachineConfigForMasterOrFail()

		matchString = "dry-run deleting rendered MachineConfig "
		if sortedMCListMaster[0].GetName() == mSpecConf {
			matchString = "Skip dry-run deleting rendered MachineConfig "
		}
		o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+sortedMCListMaster[0].GetName()), "Oldest RenderedMachineConfig is not deleted") // to check old rendered  master MC will be getting deleted

		o.Expect(NewSortedRenderedMCMaster).To(o.Equal(sortedMCListMaster), "The dry run deleted rendered MC is removed but should exist.")
		logger.Infof("OK!\n")

		// 3 To check for 'oc adm prune renderedmachineconfigs list' cmd
		exutil.By("Get the rendered machineconfigs list")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "list").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)
		o.Expect(pruneMCOutput).To(o.And(o.ContainSubstring(wSpecConf), o.ContainSubstring(mSpecConf)), "Error: Deleted in-use rendered machine configs")
		for _, mc := range sortedRenderedMCs {
			used := "Currently in use: false"
			if mc.GetName() == wSpecConf || mc.GetName() == mSpecConf {
				used = "Currently in use: true"
			}
			o.Expect(pruneMCOutput).To(o.MatchRegexp(regexp.QuoteMeta(mc.GetName()) + ".*-- .*" + regexp.QuoteMeta(used) + ".*")) // to check correct rendered MC is in-use or not
		}
		logger.Infof("OK!\n")

		// 4  To check for 'oc adm prune renderedmachineconfigs list --in-use --pool-name master' cmd
		exutil.By("To get the in use rendered machineconfigs  for each MCP")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "list", "--in-use", "--pool-name", "master").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof("%s", mSpecConf)
		mStatusConf, err := mMcp.getConfigNameOfStatus()
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("%s", mStatusConf)
		// to check renderedMC is same as `spec` and `status`
		o.Expect(pruneMCOutput).To(o.ContainSubstring("spec: "+mSpecConf), "Value for `spec` is not same as expected")
		o.Expect(pruneMCOutput).To(o.ContainSubstring("status: "+mStatusConf), "Value for `status` is not same as expected ")
		logger.Infof("%s", pruneMCOutput)
		logger.Infof("OK!\n")

		// 5 To check for `oc adm prune renderedmachineconfigs --count=1 --pool-name master --confirm` cmd
		exutil.By("To delete the  rendered machineconfigs based on count and MCP name")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--count=1", "--pool-name", "master", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		NewSortedRenderedMCMaster = mcList.GetRenderedMachineConfigForMasterOrFail()

		logger.Infof(pruneMCOutput)

		if sortedMCListMaster[0].GetName() == mSpecConf {
			matchString = "Skip deleting rendered MachineConfig "
		} else {
			matchString = "deleting rendered MachineConfig "
			for _, newMc := range NewSortedRenderedMCMaster {
				o.Expect(newMc.GetName()).NotTo(o.ContainSubstring(sortedMCListMaster[0].GetName()), "Deleted rendered MachineConfig is still present in the new list") // check expected rendered-master MC is been deleted
			}
		}
		o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+sortedMCListMaster[0].GetName()), "Oldest RenderedMachineConfig is not deleted") // check oldest rendered master MC is been deleted

		logger.Infof("OK!\n")

		// 6 To check for `oc adm prune renderedmachineconfigs --confirm` cmd
		sortedRenderedMCs = mcList.GetMCPRenderedMachineConfigsOrFail() // Get the current list of rendered machine configs
		exutil.By("To delete the  rendered machineconfigs based on count and MCP name")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)

		for _, mc := range sortedRenderedMCs {
			if mc.GetName() == mSpecConf || mc.GetName() == wSpecConf {
				matchString = "Skip deleting rendered MachineConfig "
				o.Expect(mc.Exists()).To(o.BeTrue(), "Deleted the in-use rendered MC") // check in-use rendered MC is not been deleted
			} else {
				matchString = "deleting rendered MachineConfig "
				o.Expect(mc.Exists()).To(o.BeFalse(), "The expected rendered MC is not deleted") // check expected rendered MC is been deleted
			}
			o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+mc.GetName()), "Oldest RenderedMachineConfig is not deleted")
		}

		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:73155][OTP] prune renderedmachineconfigs in updating pools", func() {
		var (
			wMcp        = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcList      = NewMachineConfigList(oc.AsAdmin())
			node        = wMcp.GetSortedNodesOrFail()[0]
			fileMode    = "420"
			fileContent = "test1"
			filePath    = "/etc/mco-test-case-73155-"
			mcName      = "mco-tc-73155-"
		)

		mcList.SortByTimestamp()                         // sort by time
		wSpecConf, specErr := wMcp.getConfigNameOfSpec() // get worker MCP name
		o.Expect(specErr).NotTo(o.HaveOccurred())

		exutil.By("Create new Machine config")
		mc := NewMachineConfig(oc.AsAdmin(), mcName+"1", MachineConfigPoolWorker)
		fileConfig := getBase64EncodedFileConfig(filePath+"1", fileContent, fileMode)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		mc.skipWaitForMcp = true // to wait to execute command

		defer func() {
			exutil.By("Check Machine Config are deleted")
			o.Expect(NewRemoteFile(node, filePath+"1")).NotTo(Exist(),
				"The file %s should NOT exists", filePath+"1")
			o.Expect(NewRemoteFile(node, filePath+"2")).NotTo(Exist(),
				"The file %s should NOT exists", filePath+"2")

			exutil.By("Check the MCP status is not been degreaded")
			wMcp.waitForComplete()
		}()

		defer mc.DeleteWithWait() // Clean up after creation
		mc.create()

		logger.Infof("OK\n")

		exutil.By("Wait for first nodes to be configured")

		o.Eventually(node.IsUpdating, "10m", "20s").Should(o.BeTrue())
		o.Eventually(node.IsUpdated, "10m", "20s").Should(o.BeTrue()) // check for first node is updated

		initialRenderedMC, specErr := wMcp.getConfigNameOfSpec() // check for new worker rendered MC configured
		o.Expect(specErr).NotTo(o.HaveOccurred())
		logger.Infof("OK\n")

		exutil.By("Create new second Machine configs")

		fileConfig = getBase64EncodedFileConfig(filePath+"2", fileContent, fileMode)
		mc = NewMachineConfig(oc.AsAdmin(), mcName+"2", MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		mc.skipWaitForMcp = true // to wait to execute command

		defer mc.Delete() // Clean up after creation
		mc.create()
		logger.Infof("OK\n")

		exutil.By("Run prune command and check new rendered MC is generated with MCP is still updating")

		o.Eventually(wMcp.getConfigNameOfSpec, "5m", "20s").ShouldNot(o.Equal(initialRenderedMC), "Second worker renderedMC is not configured yet")
		newRenderedMC, specErr := wMcp.getConfigNameOfSpec()
		o.Expect(specErr).NotTo(o.HaveOccurred(), "Get desired MC of worker pool failed")

		pruneMCOutput, err := oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--pool-name", "worker", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)

		renderedMCs := []string{wSpecConf, initialRenderedMC, newRenderedMC}
		// as  wMCP is still updating with previous in-use  rendered MC and new generated MC from 1st and 2nd MC created are also in-use so we need to check they are not deleted

		for _, mc := range renderedMCs {
			o.Expect(pruneMCOutput).To(o.ContainSubstring("Skip deleting rendered MachineConfig "+mc), "Deleted the in-use rendered MC: "+mc)
		}
		logger.Infof("OK\n")

		exutil.By("Check no worker MCP is degreaded")
		wMcp.waitForComplete()

		exutil.By("Execute the prune command again after complete update")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--pool-name", "worker", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)
		o.Expect(pruneMCOutput).To(o.ContainSubstring("Skip deleting rendered MachineConfig "+newRenderedMC), "Deleted the in-use rendered MC")

		logger.Infof("OK\n")

	})

	g.It("[PolarionID:74606][OTP] 'oc adm prune' report failures consistently when using wrong pool name", func() {
		var expectedErrorMsg = "error: MachineConfigPool with name 'fake' not found"

		_, stderr, err := oc.AsAdmin().Run("adm").Args("prune", "renderedmachineconfigs", "list", "--pool-name", "fake").Outputs()
		o.Expect(err).To(o.HaveOccurred(), "Expected oc command error to fail but it didn't")
		o.Expect(IsExecShellError(err)).To(o.BeTrue(), "Unexpected error while executing prune command. %s", err)
		exitCode, unwrapErr := UnwrapExecCode(err)
		o.Expect(unwrapErr).NotTo(o.HaveOccurred(), "Could not unwrap exit code from prune command error")
		o.Expect(exitCode).ShouldNot(o.Equal(0), "Unexpected return code when executing the prune command with a wrong pool name")
		o.Expect(stderr).To(o.Equal(expectedErrorMsg), "Unexecpted error message when using wrong pool name in the prune command")

		_, stderr, err = oc.AsAdmin().Run("adm").Args("prune", "renderedmachineconfigs", "list", "--in-use", "--pool-name", "fake").Outputs()
		o.Expect(err).To(o.HaveOccurred(), "Expected oc command error to fail but it didn't")
		o.Expect(IsExecShellError(err)).To(o.BeTrue(), "Unexpected error while executing prune command with in-use flag. %s", err)
		exitCode, unwrapErr = UnwrapExecCode(err)
		o.Expect(unwrapErr).NotTo(o.HaveOccurred(), "Could not unwrap exit code from prune command error")
		o.Expect(exitCode).ShouldNot(o.Equal(0), "Unexpected return code when executing the prune command with the in-use flag and a wrong pool name")
		o.Expect(stderr).To(o.Equal(expectedErrorMsg), "Unexecpted error message when using in-use flag and a wrong pool name in the prune command")
	})

})
