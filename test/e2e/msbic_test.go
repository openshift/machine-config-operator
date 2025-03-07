package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	kruntime "k8s.io/apimachinery/pkg/runtime"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	opv1 "github.com/openshift/api/operator/v1"
	machineClientv1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"

	cov1helpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"

	mcoac "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
)

func TestBootImageReconciliationonSingleMachineSet(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is running on GCP platform
	if !verifyGCPPlatform(t, cs) {
		return
	}

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())
	testOnLabel := map[string]string{"test": "fake-update-on"}
	testOffLabel := map[string]string{"test": "fake-update-off"}

	// Update the machineconfiguration object to opt-in the label
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())
	labelSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, "test", "fake-update-on")
	applyLabelSelector := applymetav1.LabelSelector().WithMatchLabels(labelSelector.MatchLabels)

	p := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().
		WithManagementState("Managed").
		WithManagedBootImages(mcoac.ManagedBootImages().
			WithMachineManagers(mcoac.MachineManager().
				WithAPIGroup(opv1.MachineAPI).
				WithResource(opv1.MachineSets).
				WithSelection(mcoac.MachineManagerSelector().
					WithMode(opv1.Partial).
					WithPartial(mcoac.PartialSelector().
						WithMachineResourceSelector(applyLabelSelector))))))

	_, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})
	require.Nil(t, err, "updating machineconfiguration boot image knob failed")
	t.Logf("Updated machine configuration knob to target one machineset for boot image updates")

	// Pick a random machineset to test
	machineSetUnderTest := getRandomMachineSet(t, machineClient)
	t.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

	// Label this machineset with the test label and update it
	newMachineSet := machineSetUnderTest.DeepCopy()
	newMachineSet.SetLabels(testOnLabel)
	err = patchMachineSet(&machineSetUnderTest, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset for adding test on label failed")
	t.Logf("Added testing ON label to MachineSet %s", machineSetUnderTest.Name)

	machineSets, err := machineClient.MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err, "failed to grab machineset list")
	for _, ms := range machineSets.Items {
		verifyMachineSet(t, cs, ms, machineClient, newMachineSet.Name == ms.Name)
	}

	// Unlabel the machineset as it may be used in other tests
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")

	newMachineSet = machineSetUnderTestUpdated.DeepCopy()
	newMachineSet.SetLabels(testOffLabel)
	err = patchMachineSet(machineSetUnderTestUpdated, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset for test label failed")
	t.Logf("Added testing OFF label to MachineSet %s", machineSetUnderTestUpdated.Name)
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
	p := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithManagedBootImages(nil))
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

func TestBootImageDegradeCondition(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is running on GCP platform
	if !verifyGCPPlatform(t, cs) {
		return
	}

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())
	oref := []metav1.OwnerReference{{APIVersion: "test", Kind: "test", Name: "test", UID: "test"}}

	// Update the machineconfiguration object to opt-in all machinesets
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())
	p := mcoac.MachineConfiguration("cluster").WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed").WithManagedBootImages(mcoac.ManagedBootImages().WithMachineManagers(mcoac.MachineManager().WithAPIGroup(opv1.MachineAPI).WithResource(opv1.MachineSets).WithSelection(mcoac.MachineManagerSelector().WithMode(opv1.All)))))
	_, err := machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})
	require.Nil(t, err, "updating machineconfiguration boot image knob failed")
	t.Logf("Updated machine configuration knob to target all machinesets for boot image updates")

	// Pick a random machineset to test
	machineSetUnderTest := getRandomMachineSet(t, machineClient)
	t.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

	// Set an ownership label on this machineset
	newMachineSet := machineSetUnderTest.DeepCopy()
	newMachineSet.SetOwnerReferences(oref)
	err = patchMachineSet(&machineSetUnderTest, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset for adding owner reference failed")
	t.Logf("Added ownerreference to MachineSet %s", machineSetUnderTest.Name)

	var pollError error
	// Use a polling function as the CO may take a few seconds to update
	if err = wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, 20*time.Second, false, func(ctx context.Context) (bool, error) {
		// Check that the cluster operator is degraded
		co, getErr := cs.ConfigV1Interface.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
		require.NoError(t, getErr, "failed to grab cluster operator")

		if cov1helpers.IsStatusConditionFalse(co.Status.Conditions, osconfigv1.OperatorDegraded) {
			pollError = fmt.Errorf("Cluster Operator has not degraded")
			return false, nil
		}

		degradedCondition := cov1helpers.FindStatusCondition(co.Status.Conditions, osconfigv1.OperatorDegraded)
		if !strings.Contains(degradedCondition.Message, "error syncing MAPI MachineSet "+newMachineSet.Name+": unexpected OwnerReference: test/test") {
			pollError = fmt.Errorf("Cluster Operator condition does not have the correct message")
			return false, nil
		}

		return true, nil
	}); err != nil {
		t.Fatalf("Timed out waiting for cluster operator to degrade: %v", pollError)
	}

	t.Logf("Succesfully verified that the operator degraded")

	// Remove the ownerreference on the machineneset
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")
	newMachineSet = machineSetUnderTestUpdated.DeepCopy()
	newMachineSet.SetOwnerReferences(nil)
	err = patchMachineSet(machineSetUnderTestUpdated, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset while removing ownerreference failed")

	t.Logf("Removed ownerreference on MachineSet %s", machineSetUnderTest.Name)

	// Verify that the cluster operator is no longer degraded
	// Use a polling function as the CO may take a few seconds to update
	if err = wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, 20*time.Second, false, func(ctx context.Context) (bool, error) {
		// Check that the cluster operator is degraded
		co, getErr := cs.ConfigV1Interface.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
		require.NoError(t, getErr, "failed to grab cluster operator")

		if cov1helpers.IsStatusConditionTrue(co.Status.Conditions, osconfigv1.OperatorDegraded) {
			pollError = fmt.Errorf("Cluster Operator is still degraded")
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("Timed out waiting for cluster operator to not be degraded: %v", pollError)
	}
	t.Logf("Succesfully verified that the operator is no longer degraded")
}

func TestStubIgnitionUpgrade(t *testing.T) {

	cs := framework.NewClientSet("")

	// Check if the cluster is running on GCP platform
	if !verifyGCPPlatform(t, cs) {
		return
	}
	testStubSecretName := "test-user-data"
	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())
	// Create a 2.2 stub secret in the test cluster
	_, err := cs.CoreV1Interface.Secrets(ctrlcommon.MachineAPINamespace).Create(context.TODO(), getOldMAOSecret(testStubSecretName), metav1.CreateOptions{})
	require.Nil(t, err, "create test secret failed")
	t.Logf("Created test stub secret")
	// Update the machineconfiguration object to opt-in all machinesets
	machineConfigurationClient := mcopclientset.NewForConfigOrDie(cs.GetRestConfig())
	p := mcoac.MachineConfiguration("cluster").
		WithSpec(mcoac.MachineConfigurationSpec().
			WithManagementState("Managed").
			WithManagedBootImages(mcoac.ManagedBootImages().
				WithMachineManagers(mcoac.MachineManager().
					WithAPIGroup(opv1.MachineAPI).
					WithResource(opv1.MachineSets).
					WithSelection(mcoac.MachineManagerSelector().
						WithMode(opv1.All)))))
	_, err = machineConfigurationClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})

	require.Nil(t, err, "updating machineconfiguration boot image knob failed")

	t.Logf("Updated machine configuration knob to target all machinesets for boot image updates")

	// Pick a random machineset to test
	machineSetUnderTest := getRandomMachineSet(t, machineClient)
	t.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

	// Update machineset to point to 2.2 stub
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	err = unmarshalProviderSpec(&machineSetUnderTest, providerSpec)
	require.Nil(t, err, "failed to unmarshal Machine Set: %s", machineSetUnderTest.Name)

	newProviderSpec := providerSpec.DeepCopy()
	newProviderSpec.UserDataSecret.Name = testStubSecretName
	newMachineSet := machineSetUnderTest.DeepCopy()
	err = marshalProviderSpec(newMachineSet, newProviderSpec)
	require.Nil(t, err, "failed to marshal new Provider Spec object")

	err = patchMachineSet(&machineSetUnderTest, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset for test secret failed")

	// Ensure atleast one master node is ready
	t.Logf("Waiting until atleast one master node is ready...")
	helpers.WaitForOneMasterNodeToBeReady(t, cs)
	t.Logf("Updated MachineSet %s with stub secret", machineSetUnderTest.Name)

	// Check if secret got upgraded to spec 3
	secret, err := cs.CoreV1Interface.Secrets(ctrlcommon.MachineAPINamespace).Get(context.TODO(), testStubSecretName, metav1.GetOptions{})
	require.Nil(t, err, "get test secret failed")
	userData := secret.Data[ctrlcommon.UserDataKey]
	var userDataIgn interface{}
	err = json.Unmarshal(userData, &userDataIgn)
	require.Nil(t, err, "failed to unmarshal decoded user-data to json (secret %s): %wt", secret.Name, err)
	versionPath := []string{ctrlcommon.IgnFieldIgnition, ctrlcommon.IgnFieldVersion}
	version, _, err := unstructured.NestedString(userDataIgn.(map[string]any), versionPath...)
	require.Nil(t, err, "failed to find version field in ignition (user data secret %s): %w", secret.Name, err)
	require.Equal(t, strings.HasPrefix(version, ctrlcommon.MinimumAcceptableStubIgnitionSpec), true, "Upgraded stub ignition doesn't have the correct version")
	t.Logf("Test stub secret was upgraded correctly")

	// Restore machineset to original providerSpec
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")
	newMachineSet = machineSetUnderTestUpdated.DeepCopy()
	err = marshalProviderSpec(newMachineSet, providerSpec)
	require.Nil(t, err, "failed to marshal new Provider Spec object during restoration")
	err = patchMachineSet(machineSetUnderTestUpdated, newMachineSet, machineClient)
	require.Nil(t, err, "patching machineset during restoration failed")

	t.Logf("Restored MachineSet %s to original values", machineSetUnderTest.Name)

	// Delete test secret
	err = cs.CoreV1Interface.Secrets(ctrlcommon.MachineAPINamespace).Delete(context.TODO(), testStubSecretName, metav1.DeleteOptions{})
	require.Nil(t, err, "delete test secret failed")
	t.Logf("Test stub secret %s deleted", testStubSecretName)

}

// This function verifies if the boot image values of the MachineSet are in an expected state
func verifyMachineSet(t *testing.T, cs *framework.ClientSet, ms machinev1beta1.MachineSet, machineClient *machineClientv1beta1.MachineV1beta1Client, reconciliationExpected bool) {
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
	t.Logf("Updated build name in machineset %s to \"%s\"", ms.Name, originalBootImageValue+"-fake-update")

	// Ensure atleast one master node is ready
	t.Logf("Waiting until atleast one master node is ready...")
	helpers.WaitForOneMasterNodeToBeReady(t, cs)

	// Fetch the machineset under test again
	t.Logf("Fetching machineset/%s again...", ms.Name)
	machineSetUnderTestUpdated, err := machineClient.MachineSets("openshift-machine-api").Get(context.TODO(), ms.Name, metav1.GetOptions{})
	require.Nil(t, err, "failed to re-fetch machineset under test")

	// Verify that the boot images have been correctly reconciled to the expected value
	providerSpec = new(machinev1beta1.GCPMachineProviderSpec)
	err = unmarshalProviderSpec(machineSetUnderTestUpdated, providerSpec)
	require.Nil(t, err, "failed to unmarshal updated Machine Set: %s", machineSetUnderTestUpdated.Name)
	for _, disk := range providerSpec.Disks {
		if reconciliationExpected {
			require.Equal(t, originalBootImageValue, disk.Image, "boot images have not been updated correctly")
		} else {
			require.NotEqual(t, originalBootImageValue, disk.Image, "boot images have been unexpectedly updated")
		}
	}

	if reconciliationExpected {
		t.Logf("The boot image have been reconciled, as expected")
	} else {
		t.Logf("The boot images have not been reconciled, as expected")
		// Restore machineSet to original values in this case, as the machineset may be used by other test variants
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

// This is a spec 2 stub generated by the installer, in older versions of OCP(<4.6)
func getOldMAOSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
		Data: map[string][]byte{"disableTemplating": []byte("true"), "userData": []byte(`{"ignition":{"config":{"append":[{"source":"https://test-cluster-api:22623/config/worker"}]},"security":{"tls":{"certificateAuthorities":[{"source":"data:text/plain;charset=utf-8;base64,LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClJPT1QgQ0EgREFUQQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="}]}},"version":"2.2.0"}}`)},
	}
}
