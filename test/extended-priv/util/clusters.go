package util

import (
	"context"
	"strings"

	o "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
)

const (
	// osStreamLabelKey is the MachineSet/CPMS label that identifies the OS image stream
	osStreamLabelKey = "machineconfiguration.openshift.io/osstream"
	// supportedOSStream is the only OS stream value currently supported by the boot image controller
	// Note: This should be updated along with SupportedOSStream in pkg/controller/bootimage/boot_image_controller.go
	supportedOSStream = "rhel-9"
)

// GetInfraID returns the infra id
func GetInfraID(oc *CLI) (string, error) {
	infraID, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o", "jsonpath='{.status.infrastructureName}'").Output()
	if err != nil {
		return "", err
	}
	return strings.Trim(infraID, "'"), err
}

// IsTechPreviewNoUpgrade checks if the cluster has TechPreviewNoUpgrade feature set enabled
func IsTechPreviewNoUpgrade(oc *CLI) bool {
	featureGate, err := oc.AdminConfigClient().ConfigV1().FeatureGates().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		o.Expect(err).NotTo(o.HaveOccurred(), "could not retrieve feature-gate: %v", err)
	}

	return featureGate.Spec.FeatureSet == configv1.TechPreviewNoUpgrade
}

// IsSingleNodeTopology returns true if the cluster is a SNO cluster
func IsSingleNodeTopology(oc *CLI) bool {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.platformStatus.controlPlaneTopology}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	return output == string(configv1.SingleReplicaTopologyMode)
}

// SkipIfUnsupportedOSStreamLabel skips the test if any MachineSet in the cluster carries
// the machineconfiguration.openshift.io/osstream label with a value other than "rhel-9".
// MachineSets that do not carry the label at all are treated as compatible.
func SkipIfUnsupportedOSStreamLabel(oc *CLI) {
	mc, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create machine client")
	machineSets, err := mc.MachineV1beta1().MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to list machinesets")
	for _, ms := range machineSets.Items {
		val, present := ms.Labels[osStreamLabelKey]
		if present && val != supportedOSStream {
			e2eskipper.Skipf("MachineSet %q has %s=%s; only %s is supported",
				ms.Name, osStreamLabelKey, val, supportedOSStream)
		}
	}
}

// SkipIfCPMSHasUnsupportedOSStreamLabel skips the test if the "cluster" ControlPlaneMachineSet
// carries the machineconfiguration.openshift.io/osstream label with a value other than "rhel-9".
// A missing label or a missing CPMS is treated as compatible.
func SkipIfCPMSHasUnsupportedOSStreamLabel(oc *CLI) {
	mc, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create machine client")
	cpms, err := mc.MachineV1().ControlPlaneMachineSets("openshift-machine-api").Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		// CPMS not present — no label to check
		return
	}
	val, present := cpms.Labels[osStreamLabelKey]
	if present && val != supportedOSStream {
		e2eskipper.Skipf("ControlPlaneMachineSet %q has %s=%s; only %s is supported",
			cpms.Name, osStreamLabelKey, val, supportedOSStream)
	}
}

// SkipOnSingleNodeTopology skips the test if the cluster is using single-node topology
func SkipOnSingleNodeTopology(oc *CLI) {
	if IsSingleNodeTopology(oc) {
		e2eskipper.Skipf("This test does not apply to single-node topologies")
	}
}
