package util

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	o "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
)

const (
	// osStreamLabelKey is the MachineSet/CPMS label that identifies the OS image stream
	osStreamLabelKey = "machineconfiguration.openshift.io/osstream"
	// supportedOSStream is the only OS stream value currently supported by the boot image controller
	// Note: This should be updated along with SupportedOSStream in pkg/controller/bootimage/boot_image_controller.go
	supportedOSStream = "rhel-9"
)

// GetClusterVersion returns the cluster version as string value (Ex: 4.8) and cluster build (Ex: 4.8.0-0.nightly-2021-09-28-165247)
func GetClusterVersion(oc *CLI) (string, string, error) {
	clusterBuild, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("clusterversion", "-o", "jsonpath={..desired.version}").Output()
	if err != nil {
		return "", "", err
	}
	splitValues := strings.Split(clusterBuild, ".")
	if len(splitValues) < 2 {
		return "", "", fmt.Errorf("malformed cluster version %q: expected at least major.minor format", clusterBuild)
	}
	clusterVersion := splitValues[0] + "." + splitValues[1]
	return clusterVersion, clusterBuild, nil
}

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
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.controlPlaneTopology}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	return output == string(configv1.SingleReplicaTopologyMode)
}

// SkipIfUnsupportedOSStreamLabel skips the test if any MachineSet in the cluster carries
// the machineconfiguration.openshift.io/osstream label with a value other than "rhel-9".
// MachineSets that do not carry the label at all are treated as compatible.
func SkipIfUnsupportedOSStreamLabel(oc *CLI) {
	// The label selector matches only MachineSets that have the osstream label set to a value
	// other than the supported one. An empty result means all MachineSets are compatible.
	out, err := oc.AsAdmin().WithoutNamespace().Run("get").Args(
		"machinesets.machine.openshift.io", "-n", "openshift-machine-api",
		"-l", osStreamLabelKey+","+osStreamLabelKey+"!="+supportedOSStream,
		"-o", "jsonpath={range .items[*]}{.metadata.name}{end}",
	).Output()
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to list machinesets")
	if out != "" {
		e2eskipper.Skipf("MachineSet %q has unsupported %s; only %s is supported", out, osStreamLabelKey, supportedOSStream)
	}
}

// SkipIfCPMSHasUnsupportedOSStreamLabel skips the test if the "cluster" ControlPlaneMachineSet
// carries the machineconfiguration.openshift.io/osstream label with a value other than "rhel-9".
// A missing label or a missing CPMS is treated as compatible.
func SkipIfCPMSHasUnsupportedOSStreamLabel(oc *CLI) {
	// The label selector matches only a CPMS that has the osstream label set to an unsupported value.
	// An empty result means that the CPMS is compatible.
	out, err := oc.AsAdmin().WithoutNamespace().Run("get").Args(
		"controlplanemachinesets.machine.openshift.io", "-n", "openshift-machine-api",
		"-l", osStreamLabelKey+","+osStreamLabelKey+"!="+supportedOSStream,
		"-o", "jsonpath={range .items[*]}{.metadata.name}{end}",
	).Output()
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to list machinesets")
	if out != "" {
		e2eskipper.Skipf("ControlPlaneMachineSet %q has unsupported %s; only %s is supported", out, osStreamLabelKey, supportedOSStream)
	}
}

// SkipOnSingleNodeTopology skips the test if the cluster is using single-node topology
func SkipOnSingleNodeTopology(oc *CLI) {
	if IsSingleNodeTopology(oc) {
		e2eskipper.Skipf("This test does not apply to single-node topologies")
	}
}

// SkipIfClusterUnreachable skips the test if the cluster API is unreachable.
// This function builds a Go client directly from the kubeconfig path and does
// not depend on the CLI wrapper. This makes it safe to call BEFORE NewCLI's
// SetupProject BeforeEach, which would otherwise fail on an unreachable API
// before the test's own skip logic gets a chance to run.
func SkipIfClusterUnreachable(kubeconfigPath string) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		e2eskipper.Skipf("Cluster may be unreachable (config error): %v", err)
		return
	}
	cfg.Timeout = 10 * time.Second

	configClient, err := configv1client.NewForConfig(cfg)
	if err != nil {
		e2eskipper.Skipf("Cluster may be unreachable (client error): %v", err)
		return
	}

	_, err = configClient.ConfigV1().Infrastructures().Get(
		context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		if isConnectionError(err) {
			e2eskipper.Skipf("Cluster may be unreachable: %v", err)
			return
		}
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to get cluster infrastructure: %v", err)
	}
}

// SkipIfImageRegistryUnhealthy skips the test if the internal image registry
// is not healthy. The image registry must be available for NewCLI's
// SetupProject to provision dockercfg secrets in new namespaces. On SNO
// clusters the registry shares the single node with all other workloads and
// can remain flaky for extended periods after reboots.
//
// Like SkipIfClusterUnreachable, this function builds a client directly from
// the kubeconfig path and is safe to call BEFORE NewCLI's SetupProject.
func SkipIfImageRegistryUnhealthy(kubeconfigPath string) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		e2eskipper.Skipf("Cannot check image registry health (config error): %v", err)
		return
	}
	cfg.Timeout = 10 * time.Second

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		e2eskipper.Skipf("Cannot check image registry health (client error): %v", err)
		return
	}

	configClient, err := configv1client.NewForConfig(cfg)
	if err != nil {
		e2eskipper.Skipf("Cannot check image registry health (config client error): %v", err)
		return
	}

	deploy, err := kubeClient.AppsV1().Deployments("openshift-image-registry").Get(
		context.TODO(), "image-registry", metav1.GetOptions{})
	if err != nil {
		if isConnectionError(err) {
			e2eskipper.Skipf("Cannot check image registry health: %v", err)
			return
		}
		if apierrors.IsNotFound(err) {
			return
		}
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to get image-registry deployment: %v", err)
	}

	if deploy.Status.AvailableReplicas == 0 {
		e2eskipper.Skipf("Image registry has no available replicas (ready: %d/%d) — "+
			"namespace setup would fail waiting for dockercfg secrets",
			deploy.Status.ReadyReplicas, *deploy.Spec.Replicas)
	}

	// The deployment can report available replicas while the ClusterOperator
	// reports degraded (e.g. chronic restart loops after SNO reboots).
	co, err := configClient.ConfigV1().ClusterOperators().Get(
		context.TODO(), "image-registry", metav1.GetOptions{})
	if err == nil {
		for _, cond := range co.Status.Conditions {
			if cond.Type == configv1.OperatorDegraded && cond.Status == configv1.ConditionTrue {
				e2eskipper.Skipf("Image registry ClusterOperator is degraded: %s", cond.Message)
			}
			if cond.Type == configv1.OperatorAvailable && cond.Status != configv1.ConditionTrue {
				e2eskipper.Skipf("Image registry ClusterOperator is not available: %s", cond.Message)
			}
		}
	}

	// The openshift-controller-manager provisions dockercfg secrets. If it
	// has no available replicas the secrets will never appear.
	ocmDeploy, err := kubeClient.AppsV1().Deployments("openshift-controller-manager").Get(
		context.TODO(), "controller-manager", metav1.GetOptions{})
	if err == nil && ocmDeploy.Status.AvailableReplicas == 0 {
		e2eskipper.Skipf("openshift-controller-manager has no available replicas (ready: %d/%d) — "+
			"it provisions dockercfg secrets for new namespaces",
			ocmDeploy.Status.ReadyReplicas, *ocmDeploy.Spec.Replicas)
	}
}

func isConnectionError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "i/o timeout") ||
		strings.Contains(errMsg, "connection reset")
}
