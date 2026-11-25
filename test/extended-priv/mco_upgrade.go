package extended

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// The explicit MCO upgrade CI flow selects [MCOUpgrade:Pre] and [MCOUpgrade:Post]
// in separate invocations. [Early] and [Late] remain for generic run-upgrade jobs.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/upgrade][Serial][Disruptive] MCO Upgrade", func() {
	defer g.GinkgoRecover()

	var (
		// init cli object, temp namespace contains prefix mco.
		// tip: don't put this in BeforeEach/JustBeforeEach, you will get error
		// "You may only call AfterEach from within a Describe, Context or When"
		oc = exutil.NewCLI("mco-upgrade", exutil.KubeConfigPath())
		// temp dir to store all test files, and it will be recycled when test is finished
		tmpdir string
		wMcp   *MachineConfigPool
	)

	g.JustBeforeEach(func() {
		tmpdir = createTmpDir()
		PreChecks(oc)
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
	})

	g.JustAfterEach(func() {
		if err := os.RemoveAll(tmpdir); err != nil {
			logger.Warnf("failed to cleanup test dir %s: %v", tmpdir, err)
		} else {
			logger.Infof("test dir %s is cleaned up", tmpdir)
		}
		specName := g.CurrentSpecReport().LeafNodeText
		if strings.Contains(specName, "PolarionID:70813") &&
			(strings.Contains(specName, "MCOUpgrade:Post") ||
				(strings.Contains(specName, "MCOUpgrade:Pre") && g.CurrentSpecReport().Failed())) {
			o.Expect(cleanupTC70813UpgradeState(oc)).NotTo(o.HaveOccurred(),
				"failed to clean up the TC 70813 upgrade state")
		}
	})

	// port=yes - 98.4% pass rate (305 runs last 60 days)
	g.It("[PolarionID:55748][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] Upgrade failed with Transaction in progress", func() {

		exutil.By("check machine config daemon log to verify no error `Transaction in progress` found")

		allNodes, getNodesErr := NewNodeList(oc.AsAdmin()).GetAllLinux()
		o.Expect(getNodesErr).NotTo(o.HaveOccurred(), "Get all linux nodes error")
		for _, node := range allNodes {
			logger.Infof("checking mcd log on %s", node.GetName())
			errLog, getLogErr := node.GetMCDaemonLogs("'Transaction in progress: (null)'")
			// GetMCDaemonLogs uses grep which returns exit status 1 when pattern not found
			// We want the pattern NOT to be found (errLog should be empty)
			// but we need to distinguish between "pattern not found" vs real errors
			if getLogErr != nil && errLog == "" {
				// This is expected - grep didn't find the error pattern (exit 1)
				logger.Infof("no 'Transaction in progress' error found (as expected)")
			} else if getLogErr == nil && errLog != "" {
				// Pattern was found - this is bad
				o.Expect(errLog).Should(o.BeEmpty(), "Transaction in progress error found, it is unexpected")
			} else if getLogErr != nil && errLog != "" {
				// Real error occurred while getting logs
				o.Expect(getLogErr).NotTo(o.HaveOccurred(), "Error getting MCD logs from %s", node.GetName())
			}
		}
	})

	// port=maybe - 90.2% pass rate (305 runs last 60 days)
	g.It("[PolarionID:59427][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] ssh keys can be migrated to new dir when node is upgraded from RHCOS8 to RHCOS9", func() {

		var (
			oldAuthorizedKeyPath = "/home/core/.ssh/authorized_key"
			newAuthorizedKeyPath = "/home/core/.ssh/authorized_keys.d/ignition"
		)

		allCoreOsNodes := NewNodeList(oc.AsAdmin()).GetAllCoreOsNodesOrFail()
		for _, node := range allCoreOsNodes {
			// Some tests are intermittently leaking a "NoExecute" taint in the nodes. When it happens this test case fails because the "debug" pod cannot run in nodes with this taint
			// In order to avoid this instability we make sure that we only check nodes where the "debug" pod can run
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Node %s is tainted with 'NoExecute'. Validation skipped.", node.GetName())
				continue
			}

			if node.GetConditionStatusByType("DiskPressure") != FalseString {
				logger.Infof("Node %s is under disk pressure. The node cannot be debugged. We skip the validation for this node", node.GetName())
				continue
			}

			exutil.By(fmt.Sprintf("check authorized key dir and file on %s", node.GetName()))
			o.Eventually(func(gm o.Gomega) {
				output, err := node.DebugNodeWithChroot("stat", oldAuthorizedKeyPath)
				gm.Expect(err).Should(o.HaveOccurred(), "old authorized key file still exists")
				gm.Expect(output).Should(o.ContainSubstring("No such file or directory"))
			}, "3m", "20s",
			).Should(o.Succeed(),
				"The old authorized key file still exists")

			output, err := node.DebugNodeWithChroot("stat", newAuthorizedKeyPath)
			o.Expect(err).ShouldNot(o.HaveOccurred(), "new authorized key file not found")
			o.Expect(output).Should(o.ContainSubstring("File: " + newAuthorizedKeyPath))
		}

	})

	// port=maybe-slow - 98.4% pass rate (305 runs last 60 days) avg 7.9m
	g.It("[PolarionID:62154][OTP][Feature:ClusterUpgrade][Early][MCOUpgrade:Pre] Don't render new MC until base MCs update", func() {
		var (
			kcName     = "mco-tc-62154-kubeletconfig"
			kcTemplate = generateTemplateAbsolutePath("generic-kubelet-config.yaml")
			crName     = "mco-tc-62154-crconfig"
			crTemplate = generateTemplateAbsolutePath("generic-container-runtime-config.yaml")

			kubeletConfig = `{"podsPerCore": 100}`
			crConfig      = `{"pidsLimit": 2048}`
		)

		if len(wMcp.GetNodesOrFail()) == 0 {
			g.Skip("Worker pool has 0 nodes configured.")
		}

		// For debugging purposes
		if err := oc.AsAdmin().WithoutNamespace().Run("get").Args("kubeletconfig,containerruntimeconfig").Execute(); err != nil {
			logger.Debugf("failed to get kubeletconfig,containerruntimeconfig (debug only): %v", err)
		}

		exutil.By("create kubelet config to add max 100 pods per core")
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		kc.create("-p", "KUBELETCONFIG="+kubeletConfig)

		exutil.By("create ContainerRuntimeConfig")
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		cr.create("-p", "CRCONFIG="+crConfig)

		exutil.By("wait for worker pool to be ready")
		wMcp.waitForComplete()

	})

	// port=maybe-slow - 98.4% pass rate (305 runs last 60 days) avg 7.9m
	g.It("[PolarionID:62154][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] Don't render new MC until base MCs update", func() {

		var (
			kcName     = "mco-tc-62154-kubeletconfig"
			kcTemplate = generateTemplateAbsolutePath("generic-kubelet-config.yaml")
			crName     = "mco-tc-62154-crconfig"
			crTemplate = generateTemplateAbsolutePath("generic-container-runtime-config.yaml")
		)

		// Skip if worker pool has no nodes
		if len(wMcp.GetNodesOrFail()) == 0 {
			g.Skip("Worker pool has 0 nodes configured.")
		}

		// For debugging purposes
		if err := oc.AsAdmin().WithoutNamespace().Run("get").Args("kubeletconfig,containerruntimeconfig").Execute(); err != nil {
			logger.Debugf("failed to get kubeletconfig,containerruntimeconfig (debug only): %v", err)
		}

		// The explicit MCO upgrade CI flow runs the MCOUpgrade:Pre tests before the
		// upgrade and runs this MCOUpgrade:Post test only after ClusterVersion reports
		// completion. Missing resources therefore mean pre-upgrade setup failed or the
		// upgrade lost them: a failure, not a skip. The no-worker skip is mirrored above.
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		o.Expect(kc).To(Exist(),
			"the MCOUpgrade:Pre phase should have created the KubeletConfig %q before the upgrade and it should have survived it", kcName)
		defer wMcp.waitForComplete()
		defer kc.Delete()

		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		o.Expect(cr).To(Exist(),
			"the MCOUpgrade:Pre phase should have created the ContainerRuntimeConfig %q before the upgrade and it should have survived it", crName)
		defer cr.Delete()

		logger.Infof("Jira issure: https://issues.redhat.com/browse/OCPBUGS-6018")
		logger.Infof("PR: https://github.com/openshift/machine-config-operator/pull/3501")

		exutil.By("check controller versions")
		rmc, err := wMcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Cannot get the MC configured for worker pool")

		// We don't check that the kubelet configuration and the container runtime configuration have the values that we configured
		// because other preCheck test cases can override it. What we need to check is that the rendered MCs generated by our resources
		// are generated by the right controller version
		// Regarding the collision with other test cases we can have a look at https://issues.redhat.com/browse/OCPQE-19001
		// The test cases we are colliding with are: OCP-45351 and OCP-45436 from NODE team

		logger.Infof("Get controller version in rendered MC %s", rmc.GetName())
		rmcCV := rmc.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/generated-by-controller-version}`)
		logger.Infof("rendered MC controller version %s", rmcCV)

		kblmc := NewMachineConfig(oc.AsAdmin(), kc.GetGeneratedMCNameOrFail(), MachineConfigPoolWorker)
		logger.Infof("Get controller version in KubeletConfig generated MC %s", kblmc.GetName())
		kblmcCV := kblmc.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/generated-by-controller-version}`)
		logger.Infof("KubeletConfig generated MC controller version %s", kblmcCV)

		crcmc := NewMachineConfig(oc.AsAdmin(), cr.GetGeneratedMCNameOrFail(), MachineConfigPoolWorker)
		logger.Infof("Get controller version in ContainerRuntimeConfig generated MC %s", crcmc.GetName())
		crcmcCV := crcmc.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/generated-by-controller-version}`)
		logger.Infof("ContainerRuntimeConfig generated MC controller version %s", crcmcCV)

		o.Expect(kblmcCV).To(o.Equal(rmcCV),
			"KubeletConfig generated MC and worker pool rendered MC should have the same Controller Version annotation")
		o.Expect(crcmcCV).To(o.Equal(rmcCV),
			"ContainerRuntimeConfig generated MC and worker pool rendered MC should have the same Controller Version annotation")

	})

	// port=yes - 98.4% pass rate (305 runs last 60 days)
	g.It("[PolarionID:64781][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] MAchine-Config-Operator should be compliant with CIS benchmark rule", func() {
		exutil.By("Verify that machine-config-opeartor pod is not using the default SA")

		serviceAccounts, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", MachineConfigNamespace, "-l", "k8s-app=machine-config-operator",
			"-o", `jsonpath={.items[*].spec.serviceAccountName}`).Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting machine-config-operator pods")
		o.Expect(serviceAccounts).NotTo(o.BeEmpty(),
			"No machine-config-operator pods found")
		o.Expect(serviceAccounts).NotTo(o.ContainSubstring("default"),
			"machine-config-operator pod is using the 'default' serviceAccountName and it should not")
		logger.Infof("OK!\n")

		exutil.By("Verify that there is no clusterrolebinding for the default ServiceAccount")

		defaultSAClusterRoleBinding := NewResource(oc.AsAdmin(), "clusterrolebinding", "default-account-openshift-machine-config-operator")
		o.Expect(defaultSAClusterRoleBinding).NotTo(Exist(),
			"The old clusterrolebinding for the 'default' service account exists and it should not exist")
		logger.Infof("OK!\n")
	})

	// port=yes - 96.4% pass rate (305 runs last 60 days)
	g.It("[PolarionID:70577][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] Run ovs-configuration.service before dnsmasq.service on Azure", g.Label("Platform:azure"), func() {
		skipTestIfSupportedPlatformNotMatched(oc.AsAdmin(), "azure")

		var (
			ovsconfigSvcName = "ovs-configuration.service"
			dnsmasqSvcName   = "dnsmasq.service"
			masterNode       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster).GetCoreOsNodesOrFail()[0] // to compatible with SNO/Compact cluster, get a coreOS node from master pool
		)

		exutil.By("Check service is enabled for ovs-configuration.service")
		o.Expect(masterNode.IsUnitEnabled(ovsconfigSvcName)).Should(o.BeTrue(), "service %s is not enabled", ovsconfigSvcName)

		exutil.By("Check service dependencies of ovs-configuration.service")
		o.Expect(masterNode.GetUnitProperties(ovsconfigSvcName)).Should(o.MatchRegexp(fmt.Sprintf(`Before=.*%s.*`, dnsmasqSvcName)), "Cannot find dependent service definition dnsmasq for ovs-configuration")
		o.Expect(masterNode.GetUnitDependencies(ovsconfigSvcName, "--before")).Should(o.ContainSubstring(dnsmasqSvcName), "Cannot find dependent service dnsmasq for ovs-configuration")

		exutil.By("Check service state of dnsmasq")
		isActive := masterNode.IsUnitActive(dnsmasqSvcName)
		isARO, err := IsAROCluster(oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error checking if cluster is ARO")
		if isARO {
			o.Expect(isActive).Should(o.BeTrue(), "on ARO cluster service %s is not active", dnsmasqSvcName)
		} else {
			o.Expect(isActive).Should(o.BeFalse(), "on normal Azure cluster service %s should be inactive", dnsmasqSvcName)
		}

	})

	// port=yes - 98.4% pass rate (305 runs last 60 days)
	g.It("[PolarionID:70813][OTP][Feature:ClusterUpgrade][Early][MCOUpgrade:Pre] ManagedBootImages update boot image of machineset", g.Label("Platform:gce", "Platform:aws"), func() {
		// Bootimages Update functionality is only available in GCP(GA) and AWS(GA)
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())
		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImages")
		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesAWS")

		var (
			tmpNamespace         = NewResource(oc.AsAdmin(), "ns", "tc-70813-tmp-namespace")
			tmpConfigMap         = NewConfigMap(oc.AsAdmin(), tmpNamespace.GetName(), "tc-70813-tmp-configmap")
			clonedMSName         = "cloned-tc-70813-label"
			labelName            = "mcotest"
			labelValue           = "update"
			machineSet           = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
			allMachineSets       = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()
		)

		exutil.By("Persist information in a configmap in a tmp namespace")
		if !tmpNamespace.Exists() {
			logger.Infof("Creating namespace %s", tmpNamespace.GetName())
			err := oc.AsAdmin().WithoutNamespace().Run("new-project").Args(tmpNamespace.GetName(), "--skip-config-write").Execute()
			o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the temporary namespace %s", tmpNamespace.GetName())
		}
		if !tmpConfigMap.Exists() {
			err := oc.AsAdmin().WithoutNamespace().Run("create").Args("-n", tmpConfigMap.GetNamespace(), "configmap", tmpConfigMap.GetName()).Execute()
			o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the temporary configmap %s", tmpConfigMap.GetName())
		}

		for _, ms := range allMachineSets {
			logger.Infof("Store bootimage of machineset %s in tmp configmap", ms.GetName())
			o.Expect(
				tmpConfigMap.SetData(ms.GetName()+"="+ms.GetCoreOsBootImageOrFail()),
			).To(o.Succeed(), "Error storing %s data in temporary configmap", ms.GetName())
		}

		logger.Infof("OK!\n")

		originalManagedBootImages, err := machineConfiguration.Get(`{.spec.managedBootImages}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error reading the original managedBootImages configuration")
		if originalManagedBootImages == "" {
			originalManagedBootImages = tc70813ManagedBootImagesAbsent
		}
		o.Expect(
			tmpConfigMap.SetData(tc70813OriginalManagedBootImagesKey+"="+originalManagedBootImages),
		).To(o.Succeed(), "Error saving the original managedBootImages configuration")

		exutil.By("Opt-in boot images update")
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, machineSet.GetAPIGroup(), labelName, labelValue),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone the first machineset twice")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		logger.Infof("Successfully created %s machineset", clonedMS.GetName())
		logger.Infof("OK!\n")

		exutil.By("Label the cloned machineset so that it is updated by MCO")
		o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMS)
		logger.Infof("OK!\n")
	})

	// port=yes - 98.4% pass rate (305 runs last 60 days)
	g.It("[PolarionID:70813][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] ManagedBootImages update boot image of machineset", g.Label("Platform:gce", "Platform:aws"), func() {
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())
		// Bootimages Update functionality is only available in GCP(GA) and AWS(GA)

		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImages")
		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesAWS")

		var (
			tmpNamespace       = NewResource(oc.AsAdmin(), "ns", "tc-70813-tmp-namespace")
			tmpConfigMap       = NewConfigMap(oc.AsAdmin(), tmpNamespace.GetName(), "tc-70813-tmp-configmap")
			clonedMSLabelName  = "cloned-tc-70813-label"
			clonedMS           = NewMachineSet(oc.AsAdmin(), MachineAPINamespace, clonedMSLabelName)
			allMachineSets     = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()
			coreosBootimagesCM = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
			currentVersion     = NewResource(oc.AsAdmin(), "ClusterVersion", "version").GetOrFail(`{.status.desired.version}`)
		)

		// The explicit MCO upgrade CI flow runs the MCOUpgrade:Pre tests before the
		// upgrade and runs this MCOUpgrade:Post test only after ClusterVersion reports
		// completion. Its valid skip conditions (platform, feature gates, worker scaling)
		// are mirrored above, so a missing MachineSet is a failure, not a skip.
		o.Expect(clonedMS).To(Exist(),
			"the MCOUpgrade:Pre phase should have created the MachineSet %q before the upgrade and it should have survived it", clonedMSLabelName)
		defer clonedMS.Delete()

		o.Expect(tmpConfigMap).To(Exist(), "The configmap with the pre-upgrade information was not found")

		exutil.By("Check that the MCO boot images ConfigMap was updated")
		o.Eventually(coreosBootimagesCM.Get, "5m", "20s").WithArguments(`{.data.MCOReleaseImageVersion}`).Should(o.Equal(currentVersion),
			"The MCO boot images configmap doesn't have the right version after the upgrade")
		logger.Infof("OK!\n")

		exutil.By("Check that the right machinesets were updated with the right bootimage and user-data secret")

		for _, ms := range allMachineSets {
			logger.Infof("Checking machineset %s", ms.GetName())
			if ms.GetName() == clonedMS.GetName() {
				currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(exutil.CheckPlatform(oc), getCurrentRegionOrFail(oc), ms.GetArchitectureOrFail(), coreosBootimagesCM)
				logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)
				logger.Infof("Machineset %s should be updated", ms.GetName())

				o.Eventually(ms.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
					"%s was NOT updated to use the right boot image", ms)
				o.Eventually(ms.GetUserDataSecret, "1m", "20s").ShouldNot(o.Equal("worker-user-data-managed"),
					"%s should NOT be using the worker-user-data-managed secret after updating the image", ms)
			} else {
				// We check that the machineset has the same boot image that we stored before the upgrade started
				logger.Infof("Machineset %s should NOT be updated", ms.GetName())
				oldCoreOsBootImaget, err := tmpConfigMap.GetDataValue(ms.GetName())
				if err != nil {
					logger.Warnf("Not checking boot image for machineset %s. No data found in the temporary configmap %s/%s for key %s", ms.GetName(), tmpConfigMap.GetNamespace(), tmpConfigMap.GetName(), ms.GetName())
					continue // We don't want to fail the test case. The new could have been added by any other test case and we don't want to collide with other test cases
				}
				logger.Infof("Old coreOsBootImage: %s", oldCoreOsBootImaget)

				o.Expect(ms.GetCoreOsBootImage()).To(o.Equal(oldCoreOsBootImaget),
					"%s was updated, but it should not be updated", ms)
			}
			logger.Infof("OK!\n")
		}

		exutil.By("Check that the updated machineset can be scaled without problems")
		defer wMcp.waitForComplete()
		defer clonedMS.ScaleTo(0)
		o.Expect(clonedMS.ScaleTo(1)).To(o.Succeed(),
			"Error scaling up MachineSet %s", clonedMS.GetName())
		logger.Infof("Waiting %s machineset for being ready", clonedMS)
		o.Eventually(clonedMS.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", clonedMS.GetName())
		// When the node is created it is still executing rpm-ostree commands before joining
		// If we delete the node (scale to 0) before MCO has fully finished its job, it can degrade the MCP
		// Hence, we wait for ndoes to be updated before reverting to the initial state
		o.Eventually(clonedMS.AllNodesUpdated, "10m", "30s").Should(o.BeTrue(), "Machineset's nodes were never updated")
		logger.Infof("OK!\n")
	})

	// port=maybe - 89.8% pass rate (305 runs last 60 days)
	g.It("[PolarionID:76216][OTP][Feature:ClusterUpgrade][Late][MCOUpgrade:Post] Scale up nodes after upgrade", func() {
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())
		var (
			machineSet   = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			clonedMSName = "cloned-tc-76216-scaleup"
		)

		exutil.By("Clone the first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer clonedMS.Delete()
		logger.Infof("Successfully created %s machineset", clonedMS.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the updated machineset can be scaled without problems")
		defer wMcp.waitForComplete()
		defer clonedMS.ScaleTo(0)
		o.Expect(clonedMS.ScaleTo(1)).To(o.Succeed(),
			"Error scaling up MachineSet %s", clonedMS.GetName())
		logger.Infof("Waiting %s machineset for being ready", clonedMS)
		o.Eventually(clonedMS.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", clonedMS.GetName())
		// When the node is created it is still executing rpm-ostree commands before joining
		// If we delete the node (scale to 0) before MCO has fully finished its job, it can degrade the MCP
		// Hence, we wait for ndoes to be updated before reverting to the initial state
		o.Eventually(clonedMS.AllNodesUpdated, "10m", "30s").Should(o.BeTrue(), "Machineset's nodes were never updated")
		logger.Infof("OK!\n")
	})

})

const (
	tc70813NamespaceName                = "tc-70813-tmp-namespace"
	tc70813ConfigMapName                = "tc-70813-tmp-configmap"
	tc70813OriginalManagedBootImagesKey = "original-managed-boot-images"
	tc70813ManagedBootImagesAbsent      = "__mco_upgrade_managed_boot_images_absent__"
	tc70813ClonedMachineSetName         = "cloned-tc-70813-label"
)

// cleanupTC70813UpgradeState restores the managedBootImages configuration that
// the [MCOUpgrade:Pre] half of TC 70813 backed up and removes the temporary
// namespace and cloned MachineSet it created. It is safe to call when the Pre
// half never ran: missing resources are treated as nothing to clean up.
func cleanupTC70813UpgradeState(oc *exutil.CLI) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := deleteTC70813ClonedMachineSet(ctx, oc); err != nil {
		return err
	}

	client := oc.AsAdmin().AdminKubeClient()
	_, err := client.CoreV1().Namespaces().Get(ctx, tc70813NamespaceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil // The pre-upgrade half was skipped, so it created no temporary namespace.
	}
	if err != nil {
		return fmt.Errorf("get TC 70813 temporary namespace: %w", err)
	}

	var cleanupErrors []error
	configMap, err := client.CoreV1().ConfigMaps(tc70813NamespaceName).Get(ctx, tc70813ConfigMapName, metav1.GetOptions{})
	canDeleteNamespace := false
	switch {
	case apierrors.IsNotFound(err):
		// The policy update only happens after the backup ConfigMap is written.
		canDeleteNamespace = true
	case err != nil:
		cleanupErrors = append(cleanupErrors, fmt.Errorf("read TC 70813 backup ConfigMap: %w", err))
	default:
		original, found := configMap.Data[tc70813OriginalManagedBootImagesKey]
		if !found {
			// The policy update only happens after the original value is saved.
			canDeleteNamespace = true
		} else {
			machineConfiguration := GetMachineConfiguration(oc.AsAdmin())
			if original == tc70813ManagedBootImagesAbsent {
				err = machineConfiguration.RemoveManagedBootImagesConfig()
			} else {
				err = machineConfiguration.Patch("merge", fmt.Sprintf(`{"spec":{"managedBootImages":%s}}`, original))
			}
			if err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("restore managedBootImages configuration: %w", err))
			} else {
				canDeleteNamespace = true
			}
		}
	}
	if canDeleteNamespace {
		if err := client.CoreV1().Namespaces().Delete(ctx, tc70813NamespaceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete TC 70813 temporary namespace: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

// deleteTC70813ClonedMachineSet deletes the MachineSet cloned by the Pre half
// of TC 70813, but only once it has no replicas left, so that no node managed
// by the clone is orphaned by the cleanup.
func deleteTC70813ClonedMachineSet(ctx context.Context, oc *exutil.CLI) error {
	client, err := dynamic.NewForConfig(oc.AsAdmin().AdminConfig())
	if err != nil {
		return fmt.Errorf("create client for TC 70813 MachineSet cleanup: %w", err)
	}
	machineSets := client.Resource(schema.GroupVersionResource{
		Group:    MAPIApiGroup,
		Version:  "v1beta1",
		Resource: MachineSetResource,
	}).Namespace(MachineAPINamespace)

	cloned, err := machineSets.Get(ctx, tc70813ClonedMachineSetName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get TC 70813 cloned MachineSet before cleanup: %w", err)
	}
	replicas, found, err := unstructured.NestedInt64(cloned.Object, "spec", "replicas")
	if err != nil {
		return fmt.Errorf("read replica count for TC 70813 cloned MachineSet: %w", err)
	}
	if !found {
		return fmt.Errorf("TC 70813 cloned MachineSet has no spec.replicas value")
	}
	if replicas != 0 {
		return fmt.Errorf("TC 70813 cloned MachineSet still has %d replicas; leaving it and the saved configuration in place until its nodes are safe to remove", replicas)
	}
	if err := machineSets.Delete(ctx, tc70813ClonedMachineSetName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete TC 70813 cloned MachineSet: %w", err)
	}
	return nil
}
