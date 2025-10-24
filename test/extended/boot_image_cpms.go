package extended

import (
	"context"
	"fmt"

	osconfigv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/yaml"

	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"

	o "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// verifyControlPlaneMachineSetUpdate verifies that the the boot image values of a ControlPlaneMachineSet are reconciled correctly
// nolint:dupl // I separated these from verifyMachineSetUpdate for readability
func verifyControlPlaneMachineSetUpdate(oc *exutil.CLI, cpms machinev1.ControlPlaneMachineSet, updateExpected bool) {

	newProviderSpecPatch, originalProviderSpecPatch, backdatedBootImage, originalBootImage := createFakeUpdatePatchCPMS(oc, cpms)
	err := oc.Run("patch").Args(ControlPlaneMachinesetQualifiedName, cpms.Name, "-p", newProviderSpecPatch, "-n", MAPINamespace, "--type=json").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())
	defer func() {
		// Restore machineSet to original boot image as the machineset may be used by other test variants, regardless of success/fail
		err = oc.Run("patch").Args(ControlPlaneMachinesetQualifiedName, cpms.Name, "-p", originalProviderSpecPatch, "-n", MAPINamespace, "--type=json").Execute()
		o.Expect(err).NotTo(o.HaveOccurred())
		e2e.Logf("Restored build name in the machineset %s to \"%s\"", cpms.Name, originalBootImage)
	}()
	// Ensure boot image controller is not progressing
	e2e.Logf("Waiting until the boot image controller is not progressing...")
	waitForBootImageControllerToComplete(oc)

	// Fetch the providerSpec of the machineset under test again
	providerSpec, err := oc.Run("get").Args(ControlPlaneMachinesetQualifiedName, cpms.Name, "-o", "template", "--template=`{{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value}}`", "-n", MAPINamespace).Output()
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

// createFakeUpdatePatchCPMS creates an update patch for the ControlPlaneMachineSet object based on the platform
func createFakeUpdatePatchCPMS(oc *exutil.CLI, cpms machinev1.ControlPlaneMachineSet) (string, string, string, string) {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return generateAWSProviderSpecPatchCPMS(cpms)
	case osconfigv1.GCPPlatformType:
		return generateGCPProviderSpecPatchCPMS(cpms)
	case osconfigv1.AzurePlatformType:
		return generateAzureProviderSpecPatchCPMS(cpms)
	default:
		e2e.Failf("unexpected platform type; should not be here")
		return "", "", "", ""
	}
}

// generateAWSProviderSpecPatchCPMS generates a fake update patch for the AWS ControlPlaneMachineSet
func generateAWSProviderSpecPatchCPMS(cpms machinev1.ControlPlaneMachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.AWSMachineProviderConfig)
	err := unmarshalProviderSpecCPMS(&cpms, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Modify the boot image to an older known AMI value
	// See: https://issues.redhat.com/browse/OCPBUGS-57426
	originalBootImage := *providerSpec.AMI.ID
	newBootImage := "ami-000145e5a91e9ac22"
	jsonPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/ami/id", "value": "%s"}]`, newBootImage)

	// Create JSON patch to restore original AMI ID
	originalJSONPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/ami/id", "value": "%s"}]`, originalBootImage)

	return jsonPatch, originalJSONPatch, newBootImage, originalBootImage

}

// generateGCPProviderSpecPatchCPMS generates a fake update patch for the GCP ControlPlaneMachineSet
func generateGCPProviderSpecPatchCPMS(cpms machinev1.ControlPlaneMachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	err := unmarshalProviderSpecCPMS(&cpms, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Modify the boot image to a older known value.
	// See: https://issues.redhat.com/browse/OCPBUGS-57426
	originalBootImage := providerSpec.Disks[0].Image
	newBootImage := "projects/rhcos-cloud/global/images/rhcos-410-84-202210040010-0-gcp-x86-64"
	jsonPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/disks/0/image", "value": "%s"}]`, newBootImage)

	// Create JSON patch to restore original disk image
	originalJSONPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/disks/0/image", "value": "%s"}]`, originalBootImage)

	return jsonPatch, originalJSONPatch, newBootImage, originalBootImage
}

// generateAzureProviderSpecPatchCPMS generates a fake update patch for the Azure ControlPlaneMachineSet
func generateAzureProviderSpecPatchCPMS(cpms machinev1.ControlPlaneMachineSet) (string, string, string, string) {
	providerSpec := new(machinev1beta1.AzureMachineProviderSpec)
	err := unmarshalProviderSpecCPMS(&cpms, providerSpec)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Use JSON patch to precisely replace just the image field with marketplace image
	// This avoids any merge conflicts with existing fields
	// Use an older known 4.18 boot image that is available in the marketplace
	jsonPatch := `[{"op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/image", "value": {"offer": "aro4", "publisher": "azureopenshift", "resourceID": "", "sku": "418-v2", "version": "418.94.20250122", "type": "MarketplaceNoPlan"}}]`

	// Create JSON patch to restore original image
	originalImage := providerSpec.Image
	originalJSONPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/image", "value": {"offer": "%s", "publisher": "%s", "resourceID": "%s", "sku": "%s", "version": "%s", "type": "%s"}}]`,
		originalImage.Offer, originalImage.Publisher, originalImage.ResourceID, originalImage.SKU, originalImage.Version, originalImage.Type)

	return jsonPatch, originalJSONPatch, "418.94.20250122", providerSpec.Image.Version
}

// unmarshalProviderSpecCPMS unmarshals the controlplanemachineset's provider spec into
// a ProviderSpec object. Returns an error if providerSpec field is nil,
// or the unmarshal fails
func unmarshalProviderSpecCPMS(cpms *machinev1.ControlPlaneMachineSet, providerSpec interface{}) error {
	if cpms.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value == nil {
		return fmt.Errorf("providerSpec field was empty")
	}
	if err := yaml.Unmarshal(cpms.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return fmt.Errorf("unmarshal into providerSpec failed %w", err)
	}
	return nil
}
