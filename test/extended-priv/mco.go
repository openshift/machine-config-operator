package extended

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

func checkDegraded(mcp *MachineConfigPool, expectedMessage, expectedReason, degradedConditionType string, checkCODegraded bool, offset int) {
	oc := mcp.oc
	expectedNumDegradedMachines := 0
	if degradedConditionType == "NodeDegraded" {
		expectedNumDegradedMachines = 1
	}

	exutil.By("Wait until MCP becomes degraded")
	o.EventuallyWithOffset(offset, mcp, mcp.estimateWaitDuration().String(), "30s").Should(BeDegraded(),
		"The '%s' MCP should become degraded when we try to create an invalid MC, but it didn't.", mcp.GetName())
	o.EventuallyWithOffset(offset, mcp.getDegradedMachineCount, "5m", "30s").Should(o.Equal(expectedNumDegradedMachines),
		"The '%s' MCP should report '%d' degraded machine count, but it doesn't.", expectedNumDegradedMachines, mcp.GetName())

	logger.Infof("OK!\n")

	exutil.By("Validate the reported error")
	degradedCondition := mcp.GetConditionByType(degradedConditionType)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "status", o.Equal("True")),
		"'worker' MCP should report degraded status in the %s condition: %s", degradedConditionType, degradedCondition)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "message", o.MatchRegexp(expectedMessage)),
		"'worker' MCP is not reporting the expected message in the %s condition: %s", degradedConditionType, degradedCondition)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "reason", o.MatchRegexp(expectedReason)),
		"'worker' MCP is not reporting the expected reason in the NodeDegraded condition: %s", degradedConditionType, degradedCondition)
	logger.Infof("OK!\n")

	exutil.By("Get co machine config to verify status and reason for Upgradeable type")

	// If the pool is degraded, then co/machine-config should not be upgradeable
	// It's unlikely, but it can happen that the MCP is degraded, but the CO has not been already updated with the right error message.
	// So we need to poll for the right reason
	mco := NewResource(oc, "co", "machine-config")
	o.EventuallyWithOffset(offset, mco, "5m", "10s").Should(HaveConditionField("Upgradeable", "reason", o.Equal("DegradedPool")),
		"co/machine-config Upgradeable condition reason is not the expected one: %s", mco.GetConditionByType("Upgradeable"))

	o.EventuallyWithOffset(offset, mco, "5m", "10s").Should(HaveConditionField("Upgradeable", "status", o.Equal("False")),
		"co/machine-config Upgradeable condition status is not the expected one: %s", mco.GetConditionByType("Upgradeable"))

	expectedCOMessage := "One or more machine config pools are degraded, please see `oc get mcp` for further details and resolve before upgrading"
	o.EventuallyWithOffset(offset, mco, "5m", "10s").Should(HaveConditionField("Upgradeable", "message", o.ContainSubstring(expectedCOMessage)),
		"co/machine-config Upgradeable condition message is not the expected one: %s", mco.GetConditionByType("Upgradeable"))

	o.ConsistentlyWithOffset(offset, mco, "1m", "10s").Should(HaveConditionField("Available", "status", o.Equal("True")),
		"co/machine-config should never have condition Available=false")

	// Because of https://github.com/openshift/machine-config-operator/pull/4617#issuecomment-2385929278 it takes 30 minutes for the CO to become degraded
	// We cannot afford to spend 30 minutes in every negative test case just waiting for the machine-config CO to become degraded
	// We cannot afford not to test it either. We should have caught the issue linked above
	// What we will do is to check for the CO degraded status in only one of our negative test cases trying to reach a good compromise.
	if checkCODegraded {
		o.EventuallyWithOffset(offset, mco, "35m", "30s").Should(BeDegraded(),
			"co/macihne-config should be degraded because the MCP is degraded, but it wasn't")
		// Double check that when we degraded CO we didn't se a wrong Available value
		o.EventuallyWithOffset(offset, mco, "1m", "10s").Should(HaveConditionField("Available", "status", o.Equal("True")),
			"co/machine-config should never have condition Available=false")
	}
	logger.Infof("OK!\n")
}

func skipTestIfRHELVersion(node *Node, operator, constraintVersion string) {
	actualVersion, err := node.GetRHELVersion()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting RHEL version from node %s", node.GetName())

	if CompareVersions(actualVersion, operator, constraintVersion) {
		g.Skip(fmt.Sprintf("Test requires RHEL version NOT %s %s, but node has %s", operator, constraintVersion, actualVersion))
	}
}

func verifyRenderedMcs(oc *exutil.CLI, renderSuffix string, allRes []ResourceInterface) []*Resource {
	// TODO: Use MachineConfigList when MC code is refactored
	allMcs, err := NewResourceList(oc.AsAdmin(), "mc").GetAll()
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(allMcs).NotTo(o.BeEmpty())

	// cache all MCs owners to avoid too many oc binary executions while searching
	mcOwners := make(map[*Resource]gjson.Result, len(allMcs))
	for _, mc := range allMcs {
		ownersJSON := mc.GetOrFail(`{.metadata.ownerReferences}`)
		mcOwners[mc] = gjson.Parse(ownersJSON)
	}

	// Every resource should own one MC
	for _, res := range allRes {
		var ownedMc *Resource
		for mc, owners := range mcOwners {
			if owners.Exists() {
				ownersArray := owners.Array()
				for _, owner := range ownersArray {
					if !(strings.EqualFold(owner.Get("kind").String(), res.GetKind()) && strings.EqualFold(owner.Get("name").String(), res.GetName())) {
						continue
					}
					logger.Infof("Resource '%s' '%s' owns MC '%s'", res.GetKind(), res.GetName(), mc.GetName())
					// Each resource can only own one MC
					o.Expect(ownedMc).To(o.BeNil(), "Resource %s owns more than 1 MC: %s and %s", res.GetName(), mc.GetName(), ownedMc)
					ownedMc = mc
					break
				}
			} else {
				logger.Infof("MC '%s' has no owner.", mc.name)
			}

		}
		o.Expect(ownedMc).NotTo(o.BeNil(), fmt.Sprintf("Resource '%s' '%s' should have generated a MC but it has not. It owns no MC.", res.GetKind(), res.GetName()))
		o.Expect(ownedMc.name).To(o.ContainSubstring(renderSuffix), "Mc '%s' is owned by '%s' '%s' but its name does not contain the expected substring '%s'",
			ownedMc.GetName(), res.GetKind(), res.GetName(), renderSuffix)
	}

	return allMcs
}

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:42347][OTP] health check for machine-config-operator [Serial]", func() {
		exutil.By("make sure that pools are fully updated before executing the checks")
		// If any previous test created a MC, it can happen that pools are updating and will report Upgradeable=False, failing the test
		// The checks in this test case only make sense in a cluster that is not updating any pool
		mMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mMcp.waitForComplete()
		wMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		wMcp.waitForComplete()

		exutil.By("checking mco status")
		co := NewResource(oc.AsAdmin(), "co", "machine-config")
		coStatus := co.GetOrFail(`{range .status.conditions[*]}{.type}{.status}{"\n"}{end}`)
		logger.Infof(coStatus)
		o.Expect(coStatus).Should(
			o.And(
				o.ContainSubstring("ProgressingFalse"),
				o.ContainSubstring("UpgradeableTrue"),
				o.ContainSubstring("DegradedFalse"),
				o.ContainSubstring("AvailableTrue"),
			), "CO machine-config does not have the right condition status.\n%s", co.PrettyString())

		logger.Infof("machine config operator is healthy")

		exutil.By("checking mco pod status")
		o.Expect(waitForAllMCOPodsReady(oc, 5*time.Minute)).To(o.Succeed(),
			"Some MCO pods are not Ready")
		logger.Infof("mco pods are healthy")

		exutil.By("checking mcp status")
		mcp := NewResource(oc.AsAdmin(), "mcp", "")
		mcpStatus := mcp.GetOrFail(`{.items[*].status.conditions[?(@.type=="Degraded")].status}`)
		logger.Infof(mcpStatus)
		o.Expect(mcpStatus).ShouldNot(o.ContainSubstring("True"))
		logger.Infof("mcps are not degraded")

	})

	g.It("[PolarionID:43124][OTP] add machine config with invalid ignition version [Serial]", func() {
		createMcAndVerifyIgnitionVersion(oc, "invalid ign version", "change-worker-ign-version-to-invalid", "3.9.0")
	})

	g.It("[PolarionID:43726][OTP] azure Controller-Config Infrastructure does not match cluster Infrastructure resource [Serial]", func() {
		exutil.By("Get machine-config-controller platform status.")
		mccPlatformStatus := NewResource(oc.AsAdmin(), "controllerconfig", "machine-config-controller").GetOrFail("{.spec.infra.status.platformStatus}")
		logger.Infof("test mccPlatformStatus:\n %s", mccPlatformStatus)

		if exutil.CheckPlatform(oc) == AzurePlatform {
			exutil.By("check cloudName field.")

			var jsonMccPlatformStatus map[string]interface{}
			errparseinfra := json.Unmarshal([]byte(mccPlatformStatus), &jsonMccPlatformStatus)
			o.Expect(errparseinfra).NotTo(o.HaveOccurred())
			o.Expect(jsonMccPlatformStatus).Should(o.HaveKey("azure"))

			azure := jsonMccPlatformStatus["azure"].(map[string]interface{})
			o.Expect(azure).Should(o.HaveKey("cloudName"))
		}

		exutil.By("Get infrastructure platform status.")
		infraPlatformStatus := NewResource(oc.AsAdmin(), "infrastructures", "cluster").GetOrFail("{.status.platformStatus}")
		logger.Infof("infraPlatformStatus:\n %s", infraPlatformStatus)

		exutil.By("Check same status in infra and machine-config-controller.")
		o.Expect(mccPlatformStatus).To(o.Equal(infraPlatformStatus))
	})

	g.It("[PolarionID:63868][OTP] ControllerConfig sync after Infrastructure objects are updated [Disruptive]", func() {
		var (
			label      = "break.the.mco"
			labelValue = "yes-tc-63868"
			infra      = NewResource(oc.AsAdmin(), "Infrastructure", "cluster")
			mcCO       = NewResource(oc.AsAdmin(), "ClusterOperator", "machine-config")
		)

		exutil.By("Label a Infrastructure resource")
		defer func() {
			// In case of error, the machine-config ClusterOperator will become degraded,
			// so we need to recover the machine-config CO from degraded state.
			// It is done by removing the machine-config-operator pod.
			_ = infra.RemoveLabel(label)
			oc.AsAdmin().WithoutNamespace().Run("delete").Args("pod", "-n", MachineConfigNamespace,
				"-l", "k8s-app=machine-config-operator", "--ignore-not-found=true").Execute()
			o.Eventually(mcCO, "5m", "30s").ShouldNot(BeDegraded(), "Could not recover the machine-config CO from degraded status")

		}()
		o.Expect(
			infra.AddLabel(label, labelValue),
		).To(
			o.Succeed(),
			"%s/%s could not be labeled", infra.GetKind(), infra.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that machine-config ClusterOperator is not degraded")
		o.Consistently(mcCO,
			"5m", "30s").ShouldNot(BeDegraded(),
			"machine-config ClusterOperator is degraded.\n%s", mcCO.PrettyString())
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:68695][OTP] Machine-Config-Operator should not be degraded when image-registry is not installed [Serial]", func() {

		// for cluster setup, we need to use upi-on-aws + baselineCapabilitySet: None, because ipi needs known capability `machine-api`
		exutil.By("check whether capability ImageRegistry is enabled, if yes, skip the test")
		if IsCapabilityEnabled(oc, "ImageRegistry") {
			g.Skip("image registry is installed, skip this test")
		}

		exutil.By("check operator status, it should not be degraded")
		mco := NewResource(oc.AsAdmin(), "co", "machine-config")
		o.Expect(mco).ShouldNot(BeDegraded(),
			"co/machine-config Degraded condition status is not the expected one: %s", mco.GetConditionByType("Degraded"))
	})

	g.It("[PolarionID:75258][OTP] No ordering cycle issues should exist [Disruptive]", func() {
		exutil.By("Check that there are no ordering cycle problems in the nodes")
		for _, node := range OrFail[[]*Node](NewNodeList(oc.AsAdmin()).GetAllLinux()) {
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Skipping node %s since it is tainted with NoExecute and no debug pod can be run in it", node.GetName())
				continue
			}
			logger.Infof("Checking node %s", node.GetName())
			// For debugging purposes. We ignore the error here
			logMsg, _ := node.DebugNodeWithChroot("sh", "-c", `journalctl -o with-unit | grep "Found ordering cycle" -A 10 || true`)
			logger.Infof("Orderging cycle messages: %s", logMsg)

			o.Expect(node.DebugNodeWithChroot(`journalctl`, `-o`, `with-unit`)).NotTo(o.ContainSubstring("Found ordering cycle"),
				"Ordering cycle problems found in node %s", node.GetName())
		}
		logger.Infof("OK!\n")
	})
})

func createMcAndVerifyIgnitionVersion(oc *exutil.CLI, stepText, mcName, ignitionVersion string) {
	var (
		mcTemplate        = "change-worker-ign-version.yaml"
		mcp               = GetCompactCompatiblePool(oc.AsAdmin())
		expectedRDMessage = `parsing Ignition config failed: (unknown|invalid) version\. Supported spec versions: 2\.2,3\.0,3\.1,3\.2,3\.3,3\.4,3\.5$`
		expectedRDReason  = "^$" // empty string
	)
	exutil.By(fmt.Sprintf("Create machine config with %s", stepText))

	mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate(mcTemplate)
	mc.parameters = []string{"IGNITION_VERSION=" + ignitionVersion}
	mc.skipWaitForMcp = true

	validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)
}

func createMcAndVerifyMCValue(oc *exutil.CLI, stepText, mcName string, node *Node, textToVerify TextToVerify, cmd ...string) {
	exutil.By(fmt.Sprintf("Create new MC to add the %s", stepText))
	mcTemplate := mcName + ".yaml"
	mc := NewMachineConfig(oc.AsAdmin(), mcName, node.GetPrimaryPoolOrFail().GetName()).SetMCOTemplate(mcTemplate)
	defer mc.DeleteWithWait()
	// TODO: When we extract the "mcp.waitForComplete" from the "create" method, we need to take into account that if
	// we are configuring a rt-kernel we need to wait longer. Same for extensions, we need to wait longer if an extension is configured.
	mc.create()
	logger.Infof("Machine config is created successfully!")

	exutil.By(fmt.Sprintf("Check %s in the created machine config", stepText))
	mcOut, err := getMachineConfigDetails(oc, mc.name)
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(mcOut).Should(o.MatchRegexp(textToVerify.textToVerifyForMC))
	logger.Infof("%s is verified in the created machine config!", stepText)

	exutil.By(fmt.Sprintf("Check %s in the machine config daemon", stepText))
	var podOut string
	if textToVerify.needBash { // nolint:all
		podOut, err = exutil.RemoteShPodWithBash(oc, MachineConfigNamespace, node.GetMachineConfigDaemon(), cmd...)
	} else if textToVerify.needChroot {
		podOut, err = exutil.RemoteShPodWithChroot(oc, MachineConfigNamespace, node.GetMachineConfigDaemon(), cmd...)
	} else {
		podOut, err = exutil.RemoteShPod(oc, MachineConfigNamespace, node.GetMachineConfigDaemon(), cmd...)
	}
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(podOut).Should(o.MatchRegexp(textToVerify.textToVerifyForNode))
	logger.Infof("%s is verified in the machine config daemon!", stepText)
}
