package extended

import (
	"context"
	"slices"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	OSStreamLabelKey = "machineconfiguration.openshift.io/osstream"
)

var (
	expectedOSStreams = []string{"rhel-9", "rhel-10"}
)

// These tests verify OSStreams feature gate functionality.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Disruptive][OCPFeatureGate:OSStreams]", g.Ordered, func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-os-streams", exutil.KubeConfigPath()).AsAdmin()
	)

	g.JustBeforeEach(func() {
		// Skip this test if the cluster is not using MachineAPI
		skipUnlessFunctionalMachineAPI(oc)
	})

	g.It("Machines, MachineSets, and ControlPlaneMachineSets (if applicable) are labeled with OSStream [apigroup:machineconfiguration.openshift.io]", func() {
		validateOSStreamClusterLabels(oc)
	})
})

// validateOSStreamClusterLabels checks that the Machine, MachineSet, and
// ControlPlaneMachineSet (if applicable) resources have OSStream labels
func validateOSStreamClusterLabels(oc *exutil.CLI) {
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	// Check worker & master machines
	o.Expect(validateOSStreamLabelOnMachine(machineClient)).To(o.BeTrue(), "Worker and/or master Machine does not have OSStream label")
	// Check machine set
	o.Expect(validateOSStreamLabelOnMachineSet(machineClient)).To(o.BeTrue(), "MachineSet does not have OSStream label")
	// Check CPMS
	o.Expect(validateOSStreamLabelOnControlPlaneMachineSet(machineClient)).To(o.BeTrue(), "ControlPlaneMachineSet does not have OSStream label")
}

// validateOSStreamLabelOnMachine returns true if both worker and master Machines have the
// `"machineconfiguration.openshift.io/osstream"` label
func validateOSStreamLabelOnMachine(machineClient *machineclient.Clientset) bool {
	// Get worker & master Machines to test
	machines := getAllMachines(machineClient)

	// Check that the MachineSet has the expected label and that it's value is expected
	for _, machine := range machines {
		osStream, hasLabel := machine.Labels[OSStreamLabelKey]
		// If any Machine is missing the expected label or its value is not an expected value,
		// return false
		if !hasLabel || !slices.Contains(expectedOSStreams, osStream) {
			return false
		}
	}

	return true
}

// validateOSStreamLabelOnMachineSet returns true if the MachineSet has the
// `"machineconfiguration.openshift.io/osstream"` label
func validateOSStreamLabelOnMachineSet(machineClient *machineclient.Clientset) bool {
	// Get the MachineSets from the cluster
	machineSets := getAllMachineSets(machineClient)

	// Check that the MachineSet has the expected label and that it's value is expected
	for _, machineSet := range machineSets.Items {
		// Check that the labels are set in the resource's metadata
		osStream, hasLabel := machineSet.Labels[OSStreamLabelKey]
		if !hasLabel || !slices.Contains(expectedOSStreams, osStream) {
			return false
		}

		// Check that the labels are set in the spec's template
		osStream, hasLabel = machineSet.Spec.Template.Labels[OSStreamLabelKey]
		if !hasLabel || !slices.Contains(expectedOSStreams, osStream) {
			return false
		}
	}

	return true
}

// validateOSStreamLabelOnControlPlaneMachineSet returns true if either
//   - The cluster does not have a ControlPlaneMachineSet
//   - The cluster has a ControlPlaneMachineSet resource and it has the
//     `"machineconfiguration.openshift.io/osstream"` label
func validateOSStreamLabelOnControlPlaneMachineSet(machineClient *machineclient.Clientset) bool {
	// Get the ControlPlaneMachineSet
	cpms, err := getControlPlaneMachineSetIfExists(machineClient)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Some platforms do not have a ControlPlaneMachineSet, so skip in this case
	if cpms == nil {
		logger.Infof("Cluster does not have a ControlPlaneMachineSet, skipping label check.")
		return true
	}

	// Check that the ControlPlaneMachineSet has the expected label and that it's value is
	// expected in both the resource's metadata & spec's template
	osStream, hasLabel := cpms.Labels[OSStreamLabelKey]
	osStreamTemp, hasLabelTemp := cpms.Spec.Template.OpenShiftMachineV1Beta1Machine.ObjectMeta.Labels[OSStreamLabelKey]
	return hasLabel && slices.Contains(expectedOSStreams, osStream) && hasLabelTemp && slices.Contains(expectedOSStreams, osStreamTemp)
}

// getAllMachines returns all the machines on the cluster
func getAllMachines(machineClient *machineclient.Clientset) []machinev1beta1.Machine {
	machines, err := machineClient.MachineV1beta1().Machines("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Error listing machines")
	o.Expect(machines.Items).NotTo(o.BeEmpty(), "No machines found")

	return machines.Items
}

// getControlPlaneMachineSetIfExists returns the cluster's ControlPlaneMachineSet if it exists
func getControlPlaneMachineSetIfExists(machineClient *machineclient.Clientset) (*machinev1.ControlPlaneMachineSet, error) {
	cpms, err := machineClient.MachineV1().ControlPlaneMachineSets("openshift-machine-api").Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return cpms, nil
}
