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

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineClientv1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"

	osconfigv1 "github.com/openshift/api/config/v1"
)

func TestBootImageReconciliation(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is runnning on GCP platform
	infra, err := cs.ConfigV1Interface.Infrastructures().Get(context.TODO(), "cluster", metav1.GetOptions{})
	require.NoError(t, err, "failed to grab cluster infrastructure")
	if infra.Status.PlatformStatus.Type != osconfigv1.GCPPlatformType {
		t.Logf("This test is only applicable on the GCP platform, exiting test.")
		return
	}
	t.Logf("GCP platform detected, continuing test...")

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())

	// Pick a random machineset to test
	machineSetUnderTest := getRandomMachineSet(t, machineClient)
	t.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

	// Update the machineset with a dummy boot image value
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	err = unmarshalProviderSpec(&machineSetUnderTest, providerSpec)
	require.Nil(t, err, "failed to unmarshal Machine Set: %s", machineSetUnderTest.Name)

	originalBootImageValue := providerSpec.Disks[0].Image

	newProviderSpec := providerSpec.DeepCopy()
	for idx := range newProviderSpec.Disks {
		newProviderSpec.Disks[idx].Image = newProviderSpec.Disks[idx].Image + "-fake-update"
	}

	newMachineSet := machineSetUnderTest.DeepCopy()
	err = marshalProviderSpec(newMachineSet, newProviderSpec)
	require.Nil(t, err, "failed to marshal new Provider Spec object")

	err = patchMachineSet(&machineSetUnderTest, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset failed")
	t.Logf("Updated build name in the machineset %s to \"%s\"", machineSetUnderTest.Name, originalBootImageValue+"-fake-update")

	// Ensure atleast one master node is ready
	t.Logf("Waiting until atleast one master node is ready...")
	helpers.WaitForOneMasterNodeToBeReady(t, cs)

	// Fetch the machineset under test again
	t.Logf("Fetching machineset/%s again...", machineSetUnderTest.Name)
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")

	// Verify that the boot images have been correctly reconciled to the original value
	providerSpec = new(machinev1beta1.GCPMachineProviderSpec)
	err = unmarshalProviderSpec(machineSetUnderTestUpdated, providerSpec)
	require.Nil(t, err, "failed to unmarshal updated Machine Set: %s", machineSetUnderTestUpdated.Name)
	for _, disk := range providerSpec.Disks {
		require.Equal(t, originalBootImageValue, disk.Image, "boot images matching failed!")
	}
	t.Logf("The boot images have been correctly reconciled to \"%s\"", originalBootImageValue)

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
