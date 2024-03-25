package e2e_techpreview_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"sigs.k8s.io/yaml"

	kruntime "k8s.io/apimachinery/pkg/runtime"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	opv1 "github.com/openshift/api/operator/v1"
	machineClientv1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"

	mcoac "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
)

func TestBootImageReconciliationonSingleMachineSet(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is running on GCP platform
	if !verifyGCPPlatform(t, cs) {
		return
	}

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())
	fakeLabel := map[string]string{"test": "fake-update"}

	// Update the machineconfiguration object to opt-in the label
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())
	p := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithManagedBootImages(mcoac.ManagedBootImages().WithMachineManagers(mcoac.MachineManager().WithAPIGroup(opv1.MachineAPI).WithResource(opv1.MachineSets).WithSelection(mcoac.MachineManagerSelector().WithMode(opv1.Partial).WithPartial(mcoac.PartialSelector().WithMachineResourceSelector(*metav1.AddLabelToSelector(&metav1.LabelSelector{}, "test", "fake-update")))))))
	_, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})
	require.Nil(t, err, "updating machineconfiguration boot image knob failed")

	t.Logf("Updated machine configuration knob to target one machineset for boot image updates")

	// Pick a random machineset to test
	machineSetUnderTest := getRandomMachineSet(t, machineClient)
	t.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

	// Label this machineset with the test label and update it
	newMachineSet := machineSetUnderTest.DeepCopy()
	newMachineSet.SetLabels(fakeLabel)
	err = patchMachineSet(&machineSetUnderTest, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset for test label failed")

	// Refetch the machineset
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")

	// Test the machine set
	verifyMachineSet(t, cs, *machineSetUnderTestUpdated, machineClient, true)

}

func TestBootImageReconciliationonAllMachineSets(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is running on GCP platform
	if !verifyGCPPlatform(t, cs) {
		return
	}

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())

	// Update the machineconfiguration object to opt-in all machinesets
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())
	p := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithManagedBootImages(mcoac.ManagedBootImages().WithMachineManagers(mcoac.MachineManager().WithAPIGroup(opv1.MachineAPI).WithResource(opv1.MachineSets).WithSelection(mcoac.MachineManagerSelector().WithMode(opv1.All)))))
	_, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})
	require.Nil(t, err, "updating machineconfiguration boot image knob failed")

	t.Logf("Updated machine configuration knob to target all machinesets for boot image updates")

	machineSets, err := machineClient.MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err, "failed to grab machineset list")

	// Test all machinesets
	for _, ms := range machineSets.Items {
		verifyMachineSet(t, cs, ms, machineClient, true)
	}

}

func TestBootImageReconciliationonNoMachineSets(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is running on GCP platform
	if !verifyGCPPlatform(t, cs) {
		return
	}

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())

	// Update the machineconfiguration object to opt-in no machinesets
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())
	p := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed"))
	_, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})
	require.Nil(t, err, "updating machineconfiguration boot image knob failed")

	t.Logf("Updated machine configuration knob to target no machinesets for boot image updates")

	machineSets, err := machineClient.MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err, "failed to grab machineset list")

	// Test that no machinesets got updated
	for _, ms := range machineSets.Items {
		verifyMachineSet(t, cs, ms, machineClient, false)
	}

}

// This function verifies if the boot image values of the MachineSet are in an expected state
func verifyMachineSet(t *testing.T, cs *framework.ClientSet, ms machinev1beta1.MachineSet, machineClient *machineClientv1beta1.MachineV1beta1Client, updated bool) {
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	err := unmarshalProviderSpec(&ms, providerSpec)
	require.Nil(t, err, "failed to unmarshal Machine Set: %s", ms.Name)

	originalBootImageValue := providerSpec.Disks[0].Image

	newProviderSpec := providerSpec.DeepCopy()
	for idx := range newProviderSpec.Disks {
		newProviderSpec.Disks[idx].Image = newProviderSpec.Disks[idx].Image + "-fake-update"
	}

	newMachineSet := ms.DeepCopy()
	err = marshalProviderSpec(newMachineSet, newProviderSpec)
	require.Nil(t, err, "failed to marshal new Provider Spec object")

	err = patchMachineSet(&ms, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset failed")
	t.Logf("Updated build name in the machineset %s to \"%s\"", ms.Name, originalBootImageValue+"-fake-update")

	// Ensure atleast one master node is ready
	t.Logf("Waiting until atleast one master node is ready...")
	helpers.WaitForOneMasterNodeToBeReady(t, cs)

	// Fetch the machineset under test again
	t.Logf("Fetching machineset/%s again...", ms.Name)
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), ms.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")

	// Verify that the boot images have been correctly reconciled to the original value
	providerSpec = new(machinev1beta1.GCPMachineProviderSpec)
	err = unmarshalProviderSpec(machineSetUnderTestUpdated, providerSpec)
	require.Nil(t, err, "failed to unmarshal updated Machine Set: %s", machineSetUnderTestUpdated.Name)
	for _, disk := range providerSpec.Disks {
		if updated {
			require.Equal(t, originalBootImageValue, disk.Image, "boot images matching failed!")
		} else {
			require.NotEqual(t, originalBootImageValue, disk.Image, "boot images matching failed!")
		}
	}
	if updated {
		t.Logf("The boot images have been reconciled to \"%s\", as expected", originalBootImageValue)
	} else {
		t.Logf("The boot images have been not reconciled to \"%s\", as expected", originalBootImageValue)

		// Restore machineSet to original values in this case, as the boot image values may be used by other test variants
		patchMachineSet(newMachineSet, &ms, machineClient)
		t.Logf("Restored build name in the machineset %s to \"%s\"", ms.Name, originalBootImageValue)
	}

}

// This function verifies that this test is running on a GCP cluster
func verifyGCPPlatform(t *testing.T, cs *framework.ClientSet) bool {
	infra, err := cs.ConfigV1Interface.Infrastructures().Get(context.TODO(), "cluster", metav1.GetOptions{})
	require.NoError(t, err, "failed to grab cluster infrastructure")
	if infra.Status.PlatformStatus.Type != osconfigv1.GCPPlatformType {
		t.Logf("This test is only applicable on the GCP platform, exiting test.")
		return false
	}
	t.Logf("GCP platform detected, continuing test...")
	return true
}

// Picks a random machineset present on the cluster
func getRandomMachineSet(t *testing.T, machineClient *machineClientv1beta1.MachineV1beta1Client) machinev1beta1.MachineSet {
	machineSets, err := machineClient.MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err, "failed to grab machineset list")

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	machineSetUnderTest := machineSets.Items[rnd.Intn(len(machineSets.Items))]
	return machineSetUnderTest
}

// This function unmarshals the machineset's provider spec into
// a ProviderSpec object. Returns an error if providerSpec field is nil,
// or the unmarshal fails
func unmarshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms.Spec.Template.Spec.ProviderSpec.Value == nil {
		return fmt.Errorf("providerSpec field was empty")
	}
	if err := yaml.Unmarshal(ms.Spec.Template.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return fmt.Errorf("unmarshal into providerSpec failedL %w", err)
	}
	return nil
}

// This function marshals the ProviderSpec object into a MachineSet object.
// Returns an error if ProviderSpec or MachineSet is nil, or if the marshal fails
func marshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms == nil {
		return fmt.Errorf("MachineSet object was nil")
	}
	if providerSpec == nil {
		return fmt.Errorf("ProviderSpec object was nil")
	}
	rawBytes, err := json.Marshal(providerSpec)
	if err != nil {
		return fmt.Errorf("marshal into machineset failed: %w", err)
	}
	ms.Spec.Template.Spec.ProviderSpec.Value = &kruntime.RawExtension{Raw: rawBytes}
	return nil
}

// This function patches the machineset object using the machineClient
// Returns an error if marshsalling or patching fails.
func patchMachineSet(oldMachineSet, newMachineSet *machinev1beta1.MachineSet, machineClient *machineClientv1beta1.MachineV1beta1Client) error {
	machineSetMarshal, err := json.Marshal(oldMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal old machineset: %w", err)
	}
	newMachineSetMarshal, err := json.Marshal(newMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal new machineset: %w", err)
	}
	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(machineSetMarshal, newMachineSetMarshal, machineSetMarshal)
	if err != nil {
		return fmt.Errorf("unable to create patch for new machineset: %w", err)
	}
	_, err = machineClient.MachineSets("openshift-machine-api").Patch(context.TODO(), oldMachineSet.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch new machineset: %w", err)
	}
	return nil
}
