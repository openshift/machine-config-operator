package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	archtranslater "github.com/coreos/stream-metadata-go/arch"
	stream "github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcopclient "github.com/openshift/client-go/operator/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"

	o "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/test/e2e/framework"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// SkipUnlessTargetPlatform skips the test if it is not running on the target platform
func skipUnlessTargetPlatform(oc *exutil.CLI, platformType osconfigv1.PlatformType) {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	if infra.Status.PlatformStatus.Type != platformType {
		e2eskipper.Skipf("This test only applies to %s platform", platformType)
	}
	// If not Azure, we can return immediately
	if infra.Status.PlatformStatus.Type != osconfigv1.AzurePlatformType {
		return
	}
	// Special consideration for Azure: we should skip for AzureStack variant
	if infra.Status.PlatformStatus.Azure != nil && infra.Status.PlatformStatus.Azure.CloudName == osconfigv1.AzureStackCloud {
		e2eskipper.Skipf("This test does not apply to AzureStack variant within the Azure platform")
	}
}

// skipUnlessFunctionalMachineAPI skips the test if the cluster is not using Machine API
func skipUnlessFunctionalMachineAPI(oc *exutil.CLI) {
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).ToNot(o.HaveOccurred())
	machines, err := machineClient.MachineV1beta1().Machines(MAPINamespace).List(context.Background(), metav1.ListOptions{LabelSelector: MAPIMasterMachineLabelSelector})
	// the machine API can be unavailable resulting in a 404 or an empty list
	if err != nil {
		if !apierrors.IsNotFound(err) {
			o.Expect(err).ToNot(o.HaveOccurred())
		}
		e2eskipper.Skipf("haven't found machines resources on the cluster, this test can be run on a platform that supports functional MachineAPI")
		return
	}
	if len(machines.Items) == 0 {
		e2eskipper.Skipf("got an empty list of machines resources from the cluster, this test can be run on a platform that supports functional MachineAPI")
		return
	}

	// we expect just a single machine to be in the Running state
	for _, machine := range machines.Items {
		phase := ptr.Deref(machine.Status.Phase, "")
		if phase == "Running" {
			return
		}
	}
	e2eskipper.Skipf("haven't found a machine in running state, this test can be run on a platform that supports functional MachineAPI")
}

// skipOnSingleNodeTopology skips the test if the cluster is using single-node topology
func skipOnSingleNodeTopology(oc *exutil.CLI) {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	if infra.Status.ControlPlaneTopology == osconfigv1.SingleReplicaTopologyMode {
		e2eskipper.Skipf("This test does not apply to single-node topologies")
	}
}

// `IsSingleNode` returns true if the cluster is using single-node topology and false otherwise
func IsSingleNode(oc *exutil.CLI) bool {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Error determining cluster infrastructure.")
	return infra.Status.ControlPlaneTopology == osconfigv1.SingleReplicaTopologyMode
}

// getRandomMachineSet picks a random machineset present on the cluster
func getRandomMachineSet(machineClient *machineclient.Clientset) machinev1beta1.MachineSet {
	machineSets, err := machineClient.MachineV1beta1().MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	// #nosec Not sure why this is needed, we are using math/rand
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	machineSetUnderTest := machineSets.Items[rnd.Intn(len(machineSets.Items))]
	return machineSetUnderTest
}

// verifyMachineSetUpdate verifies that the the boot image values of a MachineSet are reconciled correctly
func verifyMachineSetUpdate(oc *exutil.CLI, machineSet machinev1beta1.MachineSet, updateExpected bool) {

	newProviderSpecPatch, originalProviderSpecPatch, backdatedBootImage, originalBootImage := createFakeUpdatePatch(oc, machineSet)
	err := oc.Run("patch").Args(MAPIMachinesetQualifiedName, machineSet.Name, "-p", newProviderSpecPatch, "-n", MAPINamespace, "--type=json").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())
	defer func() {
		// Restore machineSet to original boot image as the machineset may be used by other test variants, regardless of success/fail
		err = oc.Run("patch").Args(MAPIMachinesetQualifiedName, machineSet.Name, "-p", originalProviderSpecPatch, "-n", MAPINamespace, "--type=json").Execute()
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Restored build name in the machineset %s to \"%s\"", machineSet.Name, originalBootImage)
	}()
	// Ensure boot image controller is not progressing
	framework.Logf("Waiting until the boot image controller is not progressing...")
	waitForBootImageControllerToComplete(oc)

	// Fetch the providerSpec of the machineset under test again
	providerSpec, err := oc.Run("get").Args(MAPIMachinesetQualifiedName, machineSet.Name, "-o", "template", "--template=`{{.spec.template.spec.providerSpec.value}}`", "-n", MAPINamespace).Output()
	o.Expect(err).NotTo(o.HaveOccurred())

	// Verify that the machineset has the expected boot image values
	// If an update is expected, the backdated boot image should not be present
	// If an update is NOT expected, the backdated boot image should still be present; ie machineset is left untouched
	if updateExpected {
		o.Expect(providerSpec).ShouldNot(o.ContainSubstring(backdatedBootImage))
	} else {
		o.Expect(providerSpec).Should(o.ContainSubstring(backdatedBootImage))
	}
}

// unmarshalProviderSpec unmarshals the machineset's provider spec into
// a ProviderSpec object. Returns an error if providerSpec field is nil,
// or the unmarshal fails
func unmarshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms.Spec.Template.Spec.ProviderSpec.Value == nil {
		return fmt.Errorf("providerSpec field was empty")
	}
	if err := yaml.Unmarshal(ms.Spec.Template.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return fmt.Errorf("unmarshal into providerSpec failed %w", err)
	}
	return nil
}

// marshalProviderSpec marshals the ProviderSpec object into a MachineSet object.
// Returns an error if ProviderSpec or MachineSet is nil, or if the marshal fails
func marshalProviderSpec(providerSpec interface{}) (string, error) {
	if providerSpec == nil {
		return "", fmt.Errorf("ProviderSpec object was nil")
	}
	rawBytes, err := json.Marshal(providerSpec)
	if err != nil {
		return "", fmt.Errorf("marshalling providerSpec failed: %w", err)
	}
	return string(rawBytes), nil
}

// createFakeUpdatePatch creates an update patch for the MachineSet object based on the platform
func createFakeUpdatePatch(oc *exutil.CLI, machineSet machinev1beta1.MachineSet) (string, string, string, string) {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return generateAWSProviderSpecPatch(machineSet)
	case osconfigv1.GCPPlatformType:
		return generateGCPProviderSpecPatch(machineSet)
	case osconfigv1.AzurePlatformType:
		return generateAzureProviderSpecPatch(machineSet)
	default:
		e2e.Failf("unexpected platform type; should not be here")
		return "", "", "", ""
	}
}

// generateAWSProviderSpecPatch generates a fake update patch for the AWS MachineSet
func generateAWSProviderSpecPatch(machineSet machinev1beta1.MachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.AWSMachineProviderConfig)
	err := unmarshalProviderSpec(&machineSet, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Modify the boot image to an older known AMI value
	// See: https://issues.redhat.com/browse/OCPBUGS-57426
	originalBootImage := *providerSpec.AMI.ID
	newBootImage := "ami-000145e5a91e9ac22"
	newProviderSpec := providerSpec.DeepCopy()
	newProviderSpec.AMI.ID = &newBootImage

	newProviderSpecPatch, err := marshalProviderSpec(newProviderSpec)
	o.Expect(err).NotTo(o.HaveOccurred())
	originalProviderSpecPatch, err := marshalProviderSpec(providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	return newProviderSpecPatch, originalProviderSpecPatch, newBootImage, originalBootImage

}

// generateGCPProviderSpecPatch generates a fake update patch for the GCP MachineSet
func generateGCPProviderSpecPatch(machineSet machinev1beta1.MachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	err := unmarshalProviderSpec(&machineSet, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Modify the boot image to a older known value.
	// See: https://issues.redhat.com/browse/OCPBUGS-57426
	originalBootImage := providerSpec.Disks[0].Image
	newBootImage := "projects/rhcos-cloud/global/images/rhcos-410-84-202210040010-0-gcp-x86-64"
	newProviderSpec := providerSpec.DeepCopy()
	for idx := range newProviderSpec.Disks {
		newProviderSpec.Disks[idx].Image = newBootImage
	}
	newProviderSpecPatch, err := marshalProviderSpec(newProviderSpec)
	o.Expect(err).NotTo(o.HaveOccurred())
	originalProviderSpecPatch, err := marshalProviderSpec(providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	return newProviderSpecPatch, originalProviderSpecPatch, newBootImage, originalBootImage
}

// generateAzureProviderSpecPatch generates a fake update patch for the Azure MachineSet
func generateAzureProviderSpecPatch(machineSet machinev1beta1.MachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.AzureMachineProviderSpec)
	err := unmarshalProviderSpec(&machineSet, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Use JSON patch to precisely replace just the image field with marketplace image
	// This avoids any merge conflicts with existing fields
	// Use an older known 4.18 boot image that is available in the marketplace
	jsonPatch := `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/image", "value": {"offer": "aro4", "publisher": "azureopenshift", "resourceID": "", "sku": "418-v2", "version": "418.94.20250122", "type": "MarketplaceNoPlan"}}]`

	// Create JSON patch to restore original image
	originalImage := providerSpec.Image
	originalJSONPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/image", "value": {"offer": "%s", "publisher": "%s", "resourceID": "%s", "sku": "%s", "version": "%s", "type": "%s"}}]`,
		originalImage.Offer, originalImage.Publisher, originalImage.ResourceID, originalImage.SKU, originalImage.Version, originalImage.Type)

	return jsonPatch, originalJSONPatch, "418.94.20250122", providerSpec.Image.Version
}

// generateLegacyAzureProviderSpecPatch generates a fake legacy update patch for the Azure MachineSet
func generateLegacyAzureProviderSpecPatch(machineSet machinev1beta1.MachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.AzureMachineProviderSpec)
	err := unmarshalProviderSpec(&machineSet, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Use JSON patch to precisely replace just the image field with resourceID
	// This avoids any merge conflicts with existing marketplace fields
	jsonPatch := `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/image", "value": {"resourceID": "test-gen2"}}]`

	// Create JSON patch to restore original image
	originalImage := providerSpec.Image
	originalJSONPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/image", "value": {"offer": "%s", "publisher": "%s", "resourceID": "%s", "sku": "%s", "version": "%s", "type": "%s"}}]`,
		originalImage.Offer, originalImage.Publisher, originalImage.ResourceID, originalImage.SKU, originalImage.Version, originalImage.Type)

	return jsonPatch, originalJSONPatch, "test-gen2", providerSpec.Image.Version
}

// waitForBootImageControllerToComplete waits until the boot image controller is no longer progressing
func waitForBootImageControllerToComplete(oc *exutil.CLI) {
	machineConfigurationClient, err := mcopclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())
	// This has a MustPassRepeatedly(3) to ensure there isn't a false positive by checking the
	// controller status too quickly after applying the fixture/labels.
	o.Eventually(func() bool {
		mcop, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Get(context.TODO(), "cluster", metav1.GetOptions{})
		if err != nil {
			framework.Logf("Failed to grab machineconfiguration object, error :%v", err)
			return false
		}
		return IsMachineConfigurationConditionFalse(mcop.Status.Conditions, opv1.MachineConfigurationBootImageUpdateProgressing)
	}, 3*time.Minute, 5*time.Second).MustPassRepeatedly(3).Should(o.BeTrue())
}

// WaitForMachineConfigurationStatus waits until the MCO syncs the operator status to the latest spec
func WaitForMachineConfigurationStatusUpdate(oc *exutil.CLI) {
	machineConfigurationClient, err := mcopclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())
	// This has a MustPassRepeatedly(3) to ensure there isn't a false positive by checking the
	// status too quickly after applying the fixture.
	o.Eventually(func() bool {
		mcop, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Get(context.TODO(), "cluster", metav1.GetOptions{})
		if err != nil {
			framework.Logf("Failed to grab machineconfiguration object, error :%v", err)
			return false
		}
		return mcop.Generation == mcop.Status.ObservedGeneration
	}, 3*time.Minute, 1*time.Second).MustPassRepeatedly(3).Should(o.BeTrue())
}

// IsMachineConfigPoolConditionTrue returns true when the conditionType is present and set to `ConditionTrue`
func IsMachineConfigPoolConditionTrue(conditions []mcfgv1.MachineConfigPoolCondition, conditionType mcfgv1.MachineConfigPoolConditionType) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// IsMachineConfigurationConditionFalse returns false when the conditionType is present and set to `ConditionFalse`
func IsMachineConfigurationConditionFalse(conditions []metav1.Condition, conditionType string) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == metav1.ConditionFalse
		}
	}
	return false
}

// IsClusterOperatorConditionTrue returns true when the conditionType is present and set to `configv1.ConditionTrue`
func IsClusterOperatorConditionTrue(conditions []osconfigv1.ClusterOperatorStatusCondition, conditionType osconfigv1.ClusterStatusConditionType) bool {
	return IsClusterOperatorConditionPresentAndEqual(conditions, conditionType, osconfigv1.ConditionTrue)
}

// IsClusterOperatorConditionFalse returns true when the conditionType is present and set to `configv1.ConditionFalse`
func IsClusterOperatorConditionFalse(conditions []osconfigv1.ClusterOperatorStatusCondition, conditionType osconfigv1.ClusterStatusConditionType) bool {
	return IsClusterOperatorConditionPresentAndEqual(conditions, conditionType, osconfigv1.ConditionFalse)
}

// IsClusterOperatorConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsClusterOperatorConditionPresentAndEqual(conditions []osconfigv1.ClusterOperatorStatusCondition, conditionType osconfigv1.ClusterStatusConditionType, status osconfigv1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// FindClusterOperatorStatusCondition finds the conditionType in conditions.
func FindClusterOperatorStatusCondition(conditions []osconfigv1.ClusterOperatorStatusCondition, conditionType osconfigv1.ClusterStatusConditionType) *osconfigv1.ClusterOperatorStatusCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// waitForOneMasterNodeToBeReady waits until atleast one master node has completed an update
func waitForOneMasterNodeToBeReady(oc *exutil.CLI) {
	machineConfigClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Eventually(func() bool {
		mcp, err := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), "master", metav1.GetOptions{})
		if err != nil {
			framework.Logf("Failed to grab machineconfigpools, error :%v", err)
			return false
		}
		// Check if the pool has atleast one updated node(mid-upgrade), or if the pool has completed the upgrade to the new config(the additional check for spec==status here is
		// to ensure we are not checking an older "Updated" condition and the MCP fields haven't caught up yet
		if (IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) && mcp.Status.UpdatedMachineCount > 0) ||
			(IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) && (mcp.Spec.Configuration.Name == mcp.Status.Configuration.Name)) {
			return true
		}
		framework.Logf("Waiting for atleast one ready control-plane node")
		return false
	}, 5*time.Minute, 10*time.Second).Should(o.BeTrue())
}

// getAzureDiskReleaseFromConfigMap extracts the azure-disk.release field from the stream ConfigMap
func getAzureDiskReleaseFromConfigMap(configMap *corev1.ConfigMap, arch string) (string, error) {

	streamData := new(stream.Stream)
	if err := json.Unmarshal([]byte(configMap.Data["stream"]), &streamData); err != nil {
		return "", fmt.Errorf("failed to parse CoreOS stream metadata: %w", err)
	}

	// Extract RHCOS stream for the architecture of this machineSet
	streamArch, err := streamData.GetArchitecture(arch)
	if err != nil {
		return "", err
	}

	// Ensure streams are available
	if streamArch.RHELCoreOSExtensions.Marketplace == nil {
		return "", fmt.Errorf("marketplace streams are not available")
	}
	if streamArch.RHELCoreOSExtensions.Marketplace.Azure == nil {
		return "", fmt.Errorf("azure marketplace streams are not available")
	}

	imageSet := streamArch.RHELCoreOSExtensions.Marketplace.Azure.NoPurchasePlan
	if imageSet == nil {
		return "", fmt.Errorf("azure unpaid marketplace streams are not available")
	}

	return imageSet.Gen2.Version, nil
}

// Returns architecture type for a given machineset
func getArchFromMachineSet(machineset *machinev1beta1.MachineSet) (arch string, err error) {

	// Valid set of machineset/node architectures
	validArchSet := sets.New("arm64", "s390x", "amd64", "ppc64le")
	// Check if the annotation enclosing arch label is present on this machineset
	archLabel, archLabelMatch := machineset.Annotations[MachineSetArchAnnotationKey]
	if archLabelMatch {
		// Parse the annotation value which may contain multiple comma-separated labels
		// Example: kubernetes.io/arch=amd64,topology.ebs.csi.aws.com/zone=eu-central-1a
		for label := range strings.SplitSeq(archLabel, ",") {
			label = strings.TrimSpace(label)
			if archLabelValue, found := strings.CutPrefix(label, ArchLabelKey); found {
				// Extract just the architecture value after "kubernetes.io/arch="
				if validArchSet.Has(archLabelValue) {
					return archtranslater.RpmArch(archLabelValue), nil
				}
				return "", fmt.Errorf("invalid architecture value found in annotation: %s", archLabelValue)
			}
		}
		return "", fmt.Errorf("kubernetes.io/arch label not found in annotation: %s", archLabel)
	}
	return "", fmt.Errorf("%s annotation not found on MachineSet %s", MachineSetArchAnnotationKey, machineset.Name)
}

// getReleaseBootImageVersion retrieves the ConfigMap and extracts the azure-disk release for the given architecture
func getReleaseBootImageVersion(oc *exutil.CLI, arch string) (string, error) {
	out, err := oc.Run("get").Args("configmap", GoldenBootImagesConfigMap, "-n", MachineConfigNamespace, "-o", "yaml").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get ConfigMap: %v", err)
	}

	// Parse the ConfigMap output and extract azure-disk release
	var configMap corev1.ConfigMap
	err = yaml.Unmarshal([]byte(out), &configMap)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal ConfigMap: %v", err)
	}

	expectedRelease, err := getAzureDiskReleaseFromConfigMap(&configMap, arch)
	if err != nil {
		return "", fmt.Errorf("failed to get azure-disk release: %v", err)
	}

	return expectedRelease, nil
}

// getAlephVersionFromNode retrieves the aleph version of a node
func getAlephVersionFromNode(oc *exutil.CLI, nodeName string) (string, error) {
	node := NewNode(oc, nodeName)
	alephPath := "/sysroot/.coreos-aleph-version.json"

	out, err := node.DebugNodeWithChroot("cat", alephPath)
	if err != nil {
		return "", fmt.Errorf("failed to read aleph version from node %s: %v", nodeName, err)
	}

	// Extract JSON content from the output
	jsonStart := strings.Index(out, "{")
	jsonEnd := strings.LastIndex(out, "}")
	if jsonStart == -1 || jsonEnd == -1 || jsonEnd < jsonStart {
		return "", fmt.Errorf("failed to find valid JSON in aleph output from node %s", nodeName)
	}

	jsonContent := out[jsonStart : jsonEnd+1]

	var alephData map[string]any
	if err := json.Unmarshal([]byte(jsonContent), &alephData); err != nil {
		return "", fmt.Errorf("failed to unmarshal aleph data from node %s: %v", nodeName, err)
	}

	// Extract just the version field
	version, exists := alephData["version"]
	if !exists {
		return "", fmt.Errorf("version field not found in aleph data from node %s", nodeName)
	}

	versionStr, ok := version.(string)
	if !ok {
		return "", fmt.Errorf("version field is not a string in aleph data from node %s", nodeName)
	}

	return versionStr, nil
}

// Applies a boot image fixture and waits for the MCO to reconcile the status
func applyMachineConfigurationFixture(oc *exutil.CLI, fixture string) {
	err := NewMCOTemplate(oc, fixture).Apply()
	o.Expect(err).NotTo(o.HaveOccurred())

	// Ensure status accounts for the fixture that was applied
	WaitForMachineConfigurationStatusUpdate(oc)
}
