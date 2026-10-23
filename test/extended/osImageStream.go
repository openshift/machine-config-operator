package extended

import (
	"context"
	"slices"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	OSStreamLabelKey = "machineconfiguration.openshift.io/osstream"
)

var expectedOSStreams = []string{"rhel-9", "rhel-10"}

// These tests verify OSStreams feature gate functionality.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Disruptive][OCPFeatureGate:OSStreams]", g.Ordered, func() {
	defer g.GinkgoRecover()

	oc := exutil.NewCLI("mco-os-streams", exutil.KubeConfigPath()).AsAdmin()

	g.JustBeforeEach(func() {
		// Skip if no MAPI MachineSets are available (e.g. compute machine management migrated to CAPI)
		exutil.SkipIfNoMAPIMachineSets(oc.AsAdmin())
	})

	g.It("Machines, MachineSets, and ControlPlaneMachineSets (if applicable) are labeled with OSStream [apigroup:machineconfiguration.openshift.io]", func() {
		validateOSStreamClusterLabels(oc)
	})

	g.It("VAP blocks MCP OS stream downgrade on rhel-10 cluster [apigroup:machineconfiguration.openshift.io]", func() {
		extpriv.SkipIfDefaultOSImageStream(oc, extpriv.OSImageStreamRHEL9)
		exutil.SkipOnSingleNodeTopology(oc)
		validateMCPOSStreamDowngradeBlocked(oc)
	})

	g.It("VAP blocks OSImageStream defaultStream downgrade from rhel-10 to rhel-9 [apigroup:machineconfiguration.openshift.io]", func() {
		extpriv.SkipIfDefaultOSImageStream(oc, extpriv.OSImageStreamRHEL9)
		validateOSImageStreamDefaultStreamDowngradeBlocked(oc)
	})

	g.It("VAP blocks rhel-9 custom MCP creation on rhel-10 cluster [apigroup:machineconfiguration.openshift.io]", func() {
		extpriv.SkipIfDefaultOSImageStream(oc, extpriv.OSImageStreamRHEL9)
		validateCustomMCPRHEL9CreationBlocked(oc)
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

// getAllMachineSets returns all the MachineSets in a cluster
func getAllMachineSets(machineClient *machineclient.Clientset) *machinev1beta1.MachineSetList {
	machineSets, err := machineClient.MachineV1beta1().MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(machineSets.Items).NotTo(o.BeEmpty(), "No MachineSets found")

	return machineSets
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

// validateMCPOSStreamDowngradeBlocked verifies that the MachineConfigPool VAP rejects
// attempts to downgrade the OS stream from rhel-10 to rhel-9, covering both:
//   - UPDATE with no prior spec on a rhel-10 default cluster
//   - UPDATE from an explicit rhel-10 spec to rhel-9
func validateMCPOSStreamDowngradeBlocked(oc *exutil.CLI) {
	origStream, _ := oc.Run("get").Args("mcp", "worker", "-o", "jsonpath={.spec.osImageStream.name}").Output()
	defer func() {
		if origStream == "" {
			_ = oc.Run("patch").Args("mcp", "worker", "--type=json",
				"-p", `[{"op":"remove","path":"/spec/osImageStream"}]`).Execute()
		} else {
			_ = oc.Run("patch").Args("mcp", "worker", "--type=merge",
				"-p", `{"spec":{"osImageStream":{"name":"`+origStream+`"}}}`).Execute()
		}
		_ = extpriv.NewMachineConfigPool(oc, "worker").WaitForUpdatedStatus()
	}()

	exutil.By("Patch worker MCP to rhel-9 with no prior spec on rhel-10 default cluster — expect VAP rejection")
	err := oc.Run("patch").Args("mcp", "worker", "--type=merge",
		"-p", `{"spec":{"osImageStream":{"name":"rhel-9"}}}`).Execute()
	o.Expect(err).To(o.HaveOccurred(), "Expected VAP to reject setting MCP to rhel-9 when DefaultStream is rhel-10")
	exitErr, ok := err.(*exutil.ExitError)
	o.Expect(ok).To(o.BeTrue(), "expected *exutil.ExitError from admission rejection, got %T: %v", err, err)
	o.Expect(exitErr.StdErr).To(o.ContainSubstring("rhel-9 is not supported when the cluster default stream is rhel-10"),
		"Expected VAP rejection message")
	logger.Infof("OK!\n")

	exutil.By("Set worker MCP to rhel-10 explicitly — expect acceptance")
	o.Expect(oc.Run("patch").Args("mcp", "worker", "--type=merge",
		"-p", `{"spec":{"osImageStream":{"name":"rhel-10"}}}`).Execute()).To(o.Succeed(),
		"Expected setting MCP to explicit rhel-10 to succeed")
	logger.Infof("OK!\n")

	exutil.By("Patch worker MCP from explicit rhel-10 to rhel-9 — expect VAP rejection")
	err = oc.Run("patch").Args("mcp", "worker", "--type=merge",
		"-p", `{"spec":{"osImageStream":{"name":"rhel-9"}}}`).Execute()
	o.Expect(err).To(o.HaveOccurred(), "Expected VAP to reject downgrading MCP from explicit rhel-10 to rhel-9")
	exitErr, ok = err.(*exutil.ExitError)
	o.Expect(ok).To(o.BeTrue(), "expected *exutil.ExitError from admission rejection, got %T: %v", err, err)
	o.Expect(exitErr.StdErr).To(o.ContainSubstring("downgrade is not supported"),
		"Expected VAP rejection message")
	logger.Infof("OK!\n")

}

// validateCustomMCPRHEL9CreationBlocked verifies that creating a custom MCP with rhel-9
// is rejected at admission on a rhel-10 default cluster.
func validateCustomMCPRHEL9CreationBlocked(oc *exutil.CLI) {
	defer func() {
		_ = oc.Run("delete").Args("mcp", "test-rhel9-downgrade", "--ignore-not-found").Execute()
		_ = extpriv.NewMachineConfigPool(oc, "worker").WaitForUpdatedStatus()
	}()

	exutil.By("Create custom MCP with rhel-9 stream — expect VAP rejection")
	_, err := extpriv.CreateCustomMCPWithStream(oc, "test-rhel9-downgrade", extpriv.OSImageStreamRHEL9, 0)
	o.Expect(err).To(o.HaveOccurred(), "Expected VAP to reject creating a rhel-9 MCP when DefaultStream is rhel-10")
	exitErr, ok := err.(*exutil.ExitError)
	o.Expect(ok).To(o.BeTrue(), "expected *exutil.ExitError from admission rejection, got %T: %v", err, err)
	o.Expect(exitErr.StdErr).To(o.ContainSubstring("rhel-9 is not supported when the cluster default stream is rhel-10"),
		"Expected VAP rejection message")
	logger.Infof("OK!\n")
}

// validateOSImageStreamDefaultStreamDowngradeBlocked verifies that the OSImageStream VAP
// rejects attempts to change spec.defaultStream from rhel-10 (or centos-10) to rhel-9.
func validateOSImageStreamDefaultStreamDowngradeBlocked(oc *exutil.CLI) {
	origDefault, _ := oc.Run("get").Args("osimagestream", "cluster", "-o", "jsonpath={.spec.defaultStream}").Output()
	defer func() {
		if origDefault != "" {
			_ = oc.Run("patch").Args("osimagestream", "cluster", "--type=merge",
				"-p", `{"spec":{"defaultStream":"`+origDefault+`"}}`).Execute()
		} else {
			_ = oc.Run("patch").Args("osimagestream", "cluster", "--type=json",
				"-p", `[{"op":"remove","path":"/spec/defaultStream"}]`).Execute()
		}
		_ = extpriv.NewMachineConfigPool(oc, "worker").WaitForUpdatedStatus()
	}()

	exutil.By("Patch OSImageStream cluster spec.defaultStream from rhel-10 to rhel-9 — expect VAP rejection")
	err := oc.Run("patch").Args("osimagestream", "cluster", "--type=merge",
		"-p", `{"spec":{"defaultStream":"rhel-9"}}`).Execute()
	o.Expect(err).To(o.HaveOccurred(), "Expected VAP to reject changing OSImageStream defaultStream from rhel-10 to rhel-9")
	exitErr, ok := err.(*exutil.ExitError)
	o.Expect(ok).To(o.BeTrue(), "expected *exutil.ExitError from admission rejection, got %T: %v", err, err)
	o.Expect(exitErr.StdErr).To(o.ContainSubstring("Changing the default OS stream from rhel-10 or centos-10 to rhel-9 is not supported"),
		"Expected OSImageStream VAP rejection message")
	logger.Infof("OK!\n")
}
