package extended

import (
	"fmt"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Kubelet and ContainerRuntime", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-kubelet-crt", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:42368][OTP] add max pods to the kubelet config [Disruptive]", func() {
		exutil.By("create kubelet config to add 500 max pods")
		var (
			mcp        = GetCompactCompatiblePool(oc.AsAdmin())
			node       = mcp.GetSortedNodesOrFail()[0]
			kcName     = "change-maxpods-kubelet-config"
			kcTemplate = generateTemplateAbsolutePath(kcName + ".yaml")
			kc         = NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		)

		defer func() {
			kc.DeleteOrFail()
			mcp.waitForComplete()
		}()
		kc.create("POOL=" + mcp.GetName())
		kc.waitUntilSuccess("10s")
		mcp.waitForComplete()
		logger.Infof("Kubelet config is created successfully!")

		exutil.By("Check max pods in the created kubelet config")
		kcOut := kc.GetOrFail(`{.spec}`)
		o.Expect(kcOut).Should(o.ContainSubstring(`"maxPods":500`))
		logger.Infof("Max pods are verified in the created kubelet config!")

		exutil.By("Check kubelet config in the worker node")
		maxPods, err := node.DebugNodeWithChroot("cat", "/etc/kubernetes/kubelet.conf")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(maxPods).Should(o.Or(o.ContainSubstring("\"maxPods\": 500"), o.ContainSubstring("maxPods: 500")))
		logger.Infof("Max pods are verified in the worker node!")
	})

	g.It("[PolarionID:42369][OTP] add container runtime config [Disruptive]", func() {

		var (
			mcp    = GetCompactCompatiblePool(oc.AsAdmin())
			node   = mcp.GetSortedNodesOrFail()[0]
			crName = "change-ctr-cr-config"
		)

		exutil.By("Create container runtime config")
		crTemplate := generateTemplateAbsolutePath(crName + ".yaml")
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		defer func() {
			cr.DeleteOrFail()
			mcp.waitForComplete()
		}()
		cr.create("POOL=" + mcp.GetName())
		mcp.waitForComplete()
		logger.Infof("Container runtime config is created successfully!")

		exutil.By("Check container runtime config values in the created config")
		o.Expect(cr.Get(`{.spec}`)).Should(
			o.And(
				o.ContainSubstring(`"logLevel":"debug"`),
				o.ContainSubstring(`"logSizeMax":"-1"`),
				o.ContainSubstring(`"pidsLimit":2048`),
				o.ContainSubstring(`"overlaySize":"8G"`),
				o.ContainSubstring(`"defaultRuntime":"runc"`)))
		logger.Infof("Container runtime config values are verified in the created config!")

		exutil.By("Check container runtime config values in the worker node")
		o.Expect(
			node.DebugNodeWithChroot("head", "-n", "7", "/etc/containers/storage.conf"),
		).Should(o.ContainSubstring("size = \"8G\""))

		o.Expect(
			node.DebugNodeWithChroot("crio", "config"),
		).Should(
			o.And(
				o.ContainSubstring("log_level = \"debug\""),
				o.ContainSubstring("pids_limit = 2048")))
		o.Expect(node.DebugNodeWithChroot("bash", "-c", "ps -ef | grep -E 'crun|runc'")).ShouldNot(o.And(o.ContainSubstring(" -r /usr/bin/crun")), "The runtime value should not be crun")
		o.Expect(node.DebugNodeWithChroot("bash", "-c", "ps -ef | grep -E 'crun|runc'")).Should(o.And(o.ContainSubstring(" -r /usr/bin/runc"), o.ContainSubstring("--root=/run/runc")), " The --root value does not align with the runtime specification")
		logger.Infof("Container runtime config values are verified in the worker node!")
	})

	g.It("[PolarionID:45239][OTP] KubeletConfig has a limit of 10 per cluster [Disruptive]", func() {
		kcsLimit := 10

		exutil.By("Pause mcp worker")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		defer mcp.pause(false)
		defer mcp.waitForComplete() // wait before unpausing, or an update will be triggered

		mcp.pause(true)

		exutil.By("Calculate number of existing KubeletConfigs")
		kcList := NewKubeletConfigList(oc.AsAdmin())
		kcs, kclErr := kcList.GetAll()
		o.Expect(kclErr).ShouldNot(o.HaveOccurred(), "Error getting existing KubeletConfig resources")
		existingKcs := len(kcs)
		logger.Infof("%d existing KubeletConfigs. We need to create %d KubeletConfigs to reach the %d configs limit",
			existingKcs, kcsLimit-existingKcs, kcsLimit)

		exutil.By(fmt.Sprintf("Create %d kubelet config to reach the limit", kcsLimit-existingKcs))
		createdKcs := []ResourceInterface{}
		kcTemplate := generateTemplateAbsolutePath("change-maxpods-kubelet-config.yaml")
		for n := existingKcs + 1; n <= kcsLimit; n++ {
			kcName := fmt.Sprintf("change-maxpods-kubelet-config-%d", n)
			kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
			defer kc.DeleteOrFail()
			kc.create()
			createdKcs = append(createdKcs, kc)
			logger.Infof("Created:\n %s", kcName)
		}

		exutil.By("Created kubeletconfigs must be successful")
		for _, kcItem := range createdKcs {
			kcItem.(*KubeletConfig).waitUntilSuccess("15s")
		}

		exutil.By(fmt.Sprintf("Check that %d machine configs were created", kcsLimit-existingKcs))
		renderedKcConfigsSuffix := "worker-generated-kubelet"
		verifyRenderedMcs(oc, renderedKcConfigsSuffix, createdKcs)

		exutil.By(fmt.Sprintf("Create a new Kubeletconfig. The %dth one", kcsLimit+1))
		kcName := fmt.Sprintf("change-maxpods-kubelet-config-%d", kcsLimit+1)
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		defer kc.DeleteOrFail()
		kc.create()

		exutil.By(fmt.Sprintf("Created kubeletconfigs over the limit must report a failure regarding the %d configs limit", kcsLimit))
		expectedMsg := fmt.Sprintf("could not get kubelet config key: max number of supported kubelet config (%d) has been reached. Please delete old kubelet configs before retrying", kcsLimit)
		kc.waitUntilFailure(expectedMsg, "10s")

		exutil.By("Created kubeletconfigs inside the limit must be successful")
		for _, kcItem := range createdKcs {
			kcItem.(*KubeletConfig).waitUntilSuccess("10s")
		}

		exutil.By("Check that only the right machine configs were created")
		// Check all ContainerRuntimeConfigs, the one created by this TC and the already existing ones too
		allKcs := []ResourceInterface{}
		allKcs = append(allKcs, createdKcs...)
		for _, kcItem := range kcs {
			key := kcItem
			allKcs = append(allKcs, key)
		}
		allMcs := verifyRenderedMcs(oc, renderedKcConfigsSuffix, allKcs)

		kcCounter := 0
		for _, mc := range allMcs {
			if strings.HasPrefix(mc.name, "99-"+renderedKcConfigsSuffix) {
				kcCounter++
			}
		}
		o.Expect(kcCounter).Should(o.Equal(10), "Only %d Kubeletconfig resources should be generated", kcsLimit)

	})

	g.It("[PolarionID:48468][OTP] ContainerRuntimeConfig has a limit of 10 per cluster [Disruptive]", func() {
		crsLimit := 10

		exutil.By("Pause mcp worker")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		defer mcp.pause(false)
		defer mcp.waitForComplete() // wait before unpausing, or an update will be triggered

		mcp.pause(true)

		exutil.By("Calculate number of existing ContainerRuntimeConfigs")
		crList := NewContainerRuntimeConfigList(oc.AsAdmin())
		crs, crlErr := crList.GetAll()
		o.Expect(crlErr).ShouldNot(o.HaveOccurred(), "Error getting existing ContainerRuntimeConfig resources")
		existingCrs := len(crs)
		logger.Infof("%d existing ContainerRuntimeConfig. We need to create %d ContainerRuntimeConfigs to reach the %d configs limit",
			existingCrs, crsLimit-existingCrs, crsLimit)

		exutil.By(fmt.Sprintf("Create %d container runtime configs to reach the limit", crsLimit-existingCrs))
		createdCrs := []ResourceInterface{}
		crTemplate := generateTemplateAbsolutePath("change-ctr-cr-config.yaml")
		for n := existingCrs + 1; n <= crsLimit; n++ {
			crName := fmt.Sprintf("change-ctr-cr-config-%d", n)
			cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
			defer cr.DeleteOrFail()
			cr.create()
			createdCrs = append(createdCrs, cr)
			logger.Infof("Created:\n %s", crName)
		}

		exutil.By("Created ContainerRuntimeConfigs must be successful")
		for _, crItem := range createdCrs {
			crItem.(*ContainerRuntimeConfig).waitUntilSuccess("10s")
		}

		exutil.By(fmt.Sprintf("Check that %d machine configs were created", crsLimit-existingCrs))
		renderedCrConfigsSuffix := "worker-generated-containerruntime"

		logger.Infof("Pre function res: %v", createdCrs)
		verifyRenderedMcs(oc, renderedCrConfigsSuffix, createdCrs)

		exutil.By(fmt.Sprintf("Create a new ContainerRuntimeConfig. The %dth one", crsLimit+1))
		crName := fmt.Sprintf("change-ctr-cr-config-%d", crsLimit+1)
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		defer cr.DeleteOrFail()
		cr.create()

		exutil.By(fmt.Sprintf("Created container runtime configs over the limit must report a failure regarding the %d configs limit", crsLimit))
		expectedMsg := fmt.Sprintf("could not get ctrcfg key: max number of supported ctrcfgs (%d) has been reached. Please delete old ctrcfgs before retrying", crsLimit)
		cr.waitUntilFailure(expectedMsg, "10s")

		exutil.By("Created kubeletconfigs inside the limit must be successful")
		for _, crItem := range createdCrs {
			crItem.(*ContainerRuntimeConfig).waitUntilSuccess("10s")
		}

		exutil.By("Check that only the right machine configs were created")
		// Check all ContainerRuntimeConfigs, the one created by this TC and the already existing ones too
		allCrs := []ResourceInterface{}
		allCrs = append(allCrs, createdCrs...)
		for _, crItem := range crs {
			key := crItem
			allCrs = append(allCrs, key)
		}

		allMcs := verifyRenderedMcs(oc, renderedCrConfigsSuffix, allCrs)

		crCounter := 0
		for _, mc := range allMcs {
			if strings.HasPrefix(mc.name, "99-"+renderedCrConfigsSuffix) {
				crCounter++
			}
		}
		o.Expect(crCounter).Should(o.Equal(10), "Only %d containerruntime resources should be generated", crsLimit)

	})

	g.It("[PolarionID:68797][OTP] Custom pool configs take priority over worker configs [Disruptive]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}

		var (
			kubeletConfPath           = "/etc/kubernetes/kubelet.conf"
			wMcp                      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			createdCustomPoolName     = "mco-test-68797"
			kcTemplate                = generateTemplateAbsolutePath("generic-kubelet-config.yaml")
			workerKcName              = "worker-tc-68797-kubeburst"
			workerKubeletConfig       = `{"kubeAPIBurst": 7000}`
			infraFirstKcName          = "infra-first-tc-68797-kubeburst"
			infraFirstKubeletConfig   = `{"kubeAPIBurst": 8000}`
			infraSecondKcName         = "infra-second-tc-68797-kubeburst"
			infraSeconddKubeletConfig = `{"kubeAPIBurst": 9000}`
		)

		// We need to create the kubeletconfig before we create the custom MCP
		// The reason is that in 2 workers clusters, if we apply the kubeletconfig after creating a custom pool
		// MCO will try to drain both workers at the same time because the kubeconfig applies to both, worker and custom pool
		exutil.By("Create worker KubeletConfig")
		wKc := NewKubeletConfig(oc.AsAdmin(), workerKcName, kcTemplate)
		defer wMcp.waitForComplete()
		defer wKc.Delete()
		wKc.create("KUBELETCONFIG="+workerKubeletConfig, "POOL="+wMcp.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for configurations to be applied in worker pool")
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Create custom MachineConfigPool")
		// In DeleteCustomMCP deffered function, when we delete a MCP, we wait first for the worker MCP to be updated.
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), createdCustomPoolName, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		logger.Infof("OK!\n")

		exutil.By("Create infra KubeletConfigs")
		logger.Infof("Create first infra KubeletConfig")
		infraFirstKc := NewKubeletConfig(oc.AsAdmin(), infraFirstKcName, kcTemplate)
		defer infraFirstKc.Delete()
		infraFirstKc.create("KUBELETCONFIG="+infraFirstKubeletConfig, "POOL="+infraMcp.GetName())

		logger.Infof("Create second infra KubeletConfig")
		infraSecondKc := NewKubeletConfig(oc.AsAdmin(), infraSecondKcName, kcTemplate)
		defer infraSecondKc.Delete()
		infraSecondKc.create("KUBELETCONFIG="+infraSeconddKubeletConfig, "POOL="+infraMcp.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for configurations to be applied in custom pool")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check kubelet configuration in worker pool")
		o.Expect(
			NewRemoteFile(wMcp.GetNodesOrFail()[0], kubeletConfPath).Read(),
		).To(
			HaveContent(o.Or(o.ContainSubstring(`"kubeAPIBurst": 7000`), o.ContainSubstring(`kubeAPIBurst: 7000`))),
		)
		logger.Infof("OK!\n")

		exutil.By("Check kubelet configuration in infra pool")
		o.Expect(
			NewRemoteFile(infraMcp.GetNodesOrFail()[0], kubeletConfPath).Read(),
		).To(o.And(
			HaveContent(o.Or(o.ContainSubstring(`"kubeAPIBurst": 9000`), o.ContainSubstring(`kubeAPIBurst: 9000`))),
			o.Not(HaveContent(o.Or(o.ContainSubstring(`"kubeAPIBurst": 8000`), o.ContainSubstring(`kubeAPIBurst: 8000`)))),
		))
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:80771][OTP] Verify applying the kubeletconfig with label MCC is not reporting error [Disruptive]", func() {
		var (
			kcName        = fmt.Sprintf("mco-tc-%s-kubeletconfig", GetCurrentTestPolarionIDNumber())
			kcTemplate    = generateTemplateAbsolutePath("generic-kubelet-config-label.yaml")
			crName        = fmt.Sprintf("mco-tc-%s-crc", GetCurrentTestPolarionIDNumber())
			crTemplate    = generateTemplateAbsolutePath("generic-container-runtime-config-label.yaml")
			kubeletConfig = `{"maxPods": 200}`
			crConfig      = `{"pidsLimit": 2048}`
			mcp           = GetCompactCompatiblePool(oc.AsAdmin())
			node          = mcp.GetNodesOrFail()[0]
			mcc           = NewController(oc.AsAdmin())
			errMsg        = fmt.Sprintf(`(?i)Error syncing machineconfigpool %s: status for KubeletConfig %s is being reported for 0, expecting it for 1`, mcp.name, mcp.name)
		)

		oc.AsAdmin().WithoutNamespace().Run("get").Args("kubeletconfig,containerruntimeconfig").Execute()

		exutil.By("Create kubelet config to add max 100 pods per core")
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		defer func() {
			kc.Delete()
			mcp.waitForComplete()
		}()
		kc.create("-p", "POOL="+mcp.GetName(), "KUBELETCONFIG="+kubeletConfig)
		logger.Infof("KubeletConfig Created!\n")

		exutil.By("Create ContainerRuntimeConfig")
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		defer cr.Delete()
		cr.create("-p", "POOL="+mcp.GetName(), "CRCONFIG="+crConfig)
		logger.Infof("ContainerRuntimeConfig Created!\n")

		exutil.By("Wait for  MCP to be ready")
		mcp.waitForComplete()

		exutil.By("Check no error is produced anymore")
		o.Eventually(mcc.GetLogs, "2m", "30s").ShouldNot(o.MatchRegexp(errMsg), "Controller is not reporting the expected error message in the log")
		logger.Infof("OK!\n")

		exutil.By("Verify that the nodes configuration files have the right values for Kubelet config")
		o.Eventually(NewRemoteFile(node, "/etc/kubernetes/kubelet.conf").Read, "2m", "30s").Should(HaveContent(o.ContainSubstring(`maxPods: 200`)), "Kubelet config was not correctly applied")
		logger.Infof("OK!\n")
	})
})
