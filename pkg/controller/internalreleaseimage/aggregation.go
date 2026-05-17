package internalreleaseimage

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

const (
	IRIStatusAllReleasesAvailable      = "AllReleasesAvailable"
	IRIStatusAPIIntNotAvailable        = "ApiIntNotAvailable"
	IRIStatusSomeNodesNotAvailable     = "SomeNodesUnavailable"
	IRIStatusSomeRegistriesUnavailable = "SomeRegistriesUnavailable"

	iriRegistryPath          = "/openshift/release-images"
	iriRegistryPingTimeout   = 5 * time.Second
	apiServerInternalURLPort = ":6443"
)

// aggregateMCNIRIStatus aggregates the IRI status from all control plane MachineConfigNodes
// and returns the cluster-wide status for each release bundle, the overall IRI status,
// and lists of degraded/not ready nodes.
func (ctrl *Controller) aggregateMCNIRIStatus(iri *mcfgv1alpha1.InternalReleaseImage) ([]mcfgv1alpha1.InternalReleaseImageBundleStatus, string, []string, []string, error) {
	mcns, err := ctrl.mcnLister.List(labels.Everything())
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("failed to list MachineConfigNodes: %w", err)
	}

	// Filter to only control plane nodes (IRI only runs on control plane)
	controlPlaneMCNs := ctrl.filterControlPlaneMCNs(mcns)

	if len(controlPlaneMCNs) == 0 {
		klog.V(2).Info("No control plane MachineConfigNodes found, skipping IRI status aggregation")
		return nil, IRIStatusAllReleasesAvailable, nil, nil, nil
	}

	sort.Slice(controlPlaneMCNs, func(i, j int) bool {
		return controlPlaneMCNs[i].Name < controlPlaneMCNs[j].Name
	})

	klog.V(4).Infof("Aggregating IRI status from %d control plane nodes", len(controlPlaneMCNs))

	// Check if api-int registry is available
	clusterDomain, apiIntRegistryHost, apiIntAvailable, err := ctrl.checkAPIIntRegistryAvailability()
	if err != nil {
		return buildAPIIntUnavailableReleases(iri.Spec.Releases, ""), IRIStatusAPIIntNotAvailable, nil, nil, nil
	}

	if !apiIntAvailable {
		klog.V(2).Info("api-int registry is not available, marking all releases as unavailable")
		return buildAPIIntUnavailableReleases(iri.Spec.Releases, apiIntRegistryHost), IRIStatusAPIIntNotAvailable, nil, nil, nil
	}

	// Process MCN releases and build release map
	result := ctrl.processMCNReleases(controlPlaneMCNs, clusterDomain)

	// Sort node lists for deterministic output
	sort.Strings(result.degradedNodes)
	sort.Strings(result.notReadyNodes)

	// Build final aggregated releases
	aggregatedReleases := buildAggregatedReleases(
		iri,
		result.releaseMap,
		result.iriStatus,
		result.degradedNodes,
		result.notReadyNodes,
		apiIntRegistryHost,
	)

	klog.V(4).Infof("Aggregation complete. IRIStatus: %s, Not ready nodes: %v, Degraded nodes: %v",
		result.iriStatus, result.notReadyNodes, result.degradedNodes)

	return aggregatedReleases, result.iriStatus, result.degradedNodes, result.notReadyNodes, nil
}

// filterControlPlaneMCNs returns only MachineConfigNodes that are control plane nodes.
// Uses the Node lister to check for control-plane labels.
func (ctrl *Controller) filterControlPlaneMCNs(mcns []*mcfgv1.MachineConfigNode) []*mcfgv1.MachineConfigNode {
	var controlPlaneMCNs []*mcfgv1.MachineConfigNode
	for _, mcn := range mcns {
		if ctrl.isControlPlaneNode(mcn.Name) {
			controlPlaneMCNs = append(controlPlaneMCNs, mcn)
		}
	}
	return controlPlaneMCNs
}

// checkAPIIntRegistryAvailability checks if the api-int registry is available.
// Returns the cluster domain, api-int registry host, and availability status.
func (ctrl *Controller) checkAPIIntRegistryAvailability() (string, string, bool, error) {
	clusterDomain, err := ctrl.getClusterDomain()
	if err != nil {
		klog.Warningf("Failed to get cluster domain: %v", err)
		return "", "", false, fmt.Errorf("failed to get cluster domain: %w", err)
	}

	cconfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		klog.Warningf("Failed to get ControllerConfig for CA cert: %v", err)
		return "", "", false, fmt.Errorf("failed to get ControllerConfig: %w", err)
	}

	apiIntRegistryHost := fmt.Sprintf("api-int.%s:%d", clusterDomain, ctrlcommon.IRIRegistryPort)
	apiIntAvailable := pingRegistry(apiIntRegistryHost, cconfig.Spec.RootCAData)
	klog.V(4).Infof("api-int registry available: %v (URL: %s)", apiIntAvailable, apiIntRegistryHost)

	return clusterDomain, apiIntRegistryHost, apiIntAvailable, nil
}

// mcnReleaseProcessingResult contains the results of processing MCN releases.
type mcnReleaseProcessingResult struct {
	releaseMap    map[string]mcfgv1alpha1.InternalReleaseImageBundleStatus
	iriStatus     string
	degradedNodes []string
	notReadyNodes []string
}

// processMCNReleases scans through MCNs and builds a release map with node health tracking.
func (ctrl *Controller) processMCNReleases(controlPlaneMCNs []*mcfgv1.MachineConfigNode, clusterDomain string) mcnReleaseProcessingResult {
	releaseMap := make(map[string]mcfgv1alpha1.InternalReleaseImageBundleStatus)
	var notReadyNodes []string
	var degradedNodes []string
	iriStatus := IRIStatusAllReleasesAvailable

	for _, mcn := range controlPlaneMCNs {
		nodeHealthy := true

		if !ctrl.isNodeReady(mcn.Name) {
			klog.V(4).Infof("Node %s is not ready", mcn.Name)
			iriStatus = IRIStatusSomeNodesNotAvailable
			notReadyNodes = append(notReadyNodes, mcn.Name)
			nodeHealthy = false
		}

		iriDegradedCond := meta.FindStatusCondition(mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded))
		if iriDegradedCond != nil && iriDegradedCond.Status == metav1.ConditionTrue {
			klog.V(4).Infof("MCN %s is degraded", mcn.Name)
			iriStatus = IRIStatusSomeRegistriesUnavailable
			degradedNodes = append(degradedNodes, mcn.Name)
			nodeHealthy = false
		}

		// Process releases from this MCN (both healthy and unhealthy nodes)
		for _, release := range mcn.Status.InternalReleaseImage.Releases {
			apiIntImage := transformToAPIIntURL(release.Image, clusterDomain)

			if _, exists := releaseMap[release.Name]; !exists {
				releaseMap[release.Name] = mcfgv1alpha1.InternalReleaseImageBundleStatus{
					Name:       release.Name,
					Image:      apiIntImage,
					Conditions: release.Conditions,
				}
			} else if nodeHealthy {
				// Prefer healthy node's conditions (overwrite with healthy status)
				klog.V(4).Infof("Overwriting release %s status with healthy version from node %s", release.Name, mcn.Name)
				releaseMap[release.Name] = mcfgv1alpha1.InternalReleaseImageBundleStatus{
					Name:       release.Name,
					Image:      apiIntImage,
					Conditions: release.Conditions,
				}
			}
		}
	}

	return mcnReleaseProcessingResult{
		releaseMap:    releaseMap,
		iriStatus:     iriStatus,
		degradedNodes: degradedNodes,
		notReadyNodes: notReadyNodes,
	}
}

// buildAggregatedReleases builds the final aggregated releases list from the release map.
func buildAggregatedReleases(
	iri *mcfgv1alpha1.InternalReleaseImage,
	releaseMap map[string]mcfgv1alpha1.InternalReleaseImageBundleStatus,
	iriStatus string,
	degradedNodes, notReadyNodes []string,
	apiIntRegistryHost string,
) []mcfgv1alpha1.InternalReleaseImageBundleStatus {
	aggregatedReleases := []mcfgv1alpha1.InternalReleaseImageBundleStatus{}

	for _, specRelease := range iri.Spec.Releases {
		if releaseStatus, exists := releaseMap[specRelease.Name]; exists {
			// Found the release in at least one MCN
			if iriStatus != IRIStatusAllReleasesAvailable {
				releaseStatus.Conditions = updateDegradedCondition(releaseStatus.Conditions, iriStatus, degradedNodes, notReadyNodes)
			}
			aggregatedReleases = append(aggregatedReleases, releaseStatus)
		} else {
			// Release not found in any MCN - mark as unavailable
			klog.V(4).Infof("Release %s not found in any MCN, marking as unavailable", specRelease.Name)

			var degradedReason, degradedMessage string
			switch {
			case len(degradedNodes) > 0:
				degradedReason = IRIStatusSomeRegistriesUnavailable
				degradedMessage = fmt.Sprintf("The following nodes are degraded: [%s]. See the related MachineConfigNode resource status for more details.", strings.Join(degradedNodes, ", "))
			case len(notReadyNodes) > 0:
				degradedReason = IRIStatusSomeNodesNotAvailable
				degradedMessage = fmt.Sprintf("The following nodes are not ready: [%s].", strings.Join(notReadyNodes, ", "))
			default:
				degradedReason = "ReleaseImageNotAvailable"
				degradedMessage = "The specified release image is not available"
			}

			imageRef := fmt.Sprintf("%s%s@sha256:%s", apiIntRegistryHost, iriRegistryPath, unavailableImageDigest)

			aggregatedReleases = append(aggregatedReleases, mcfgv1alpha1.InternalReleaseImageBundleStatus{
				Name:  specRelease.Name,
				Image: imageRef,
				Conditions: []metav1.Condition{
					{
						Type:               string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
						Status:             metav1.ConditionFalse,
						Reason:             "ReleaseImageNotAvailable",
						Message:            "The specified release image is not available",
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
						Status:             metav1.ConditionTrue,
						Reason:             degradedReason,
						Message:            degradedMessage,
						LastTransitionTime: metav1.Now(),
					},
				},
			})
		}
	}

	return aggregatedReleases
}

const (
	// unavailableImageDigest is a placeholder SHA256 digest used when the actual image
	// digest cannot be determined (e.g., when the registry is unreachable).
	unavailableImageDigest = "0000000000000000000000000000000000000000000000000000000000000000"
)

// buildAPIIntUnavailableReleases creates release statuses when api-int is not available
func buildAPIIntUnavailableReleases(specReleases []mcfgv1alpha1.InternalReleaseImageRef, apiIntRegistry string) []mcfgv1alpha1.InternalReleaseImageBundleStatus {
	releases := []mcfgv1alpha1.InternalReleaseImageBundleStatus{}

	for _, specRelease := range specReleases {
		// Construct a valid OCI image reference (even though registry is unreachable)
		imageRef := fmt.Sprintf("%s%s@sha256:%s", apiIntRegistry, iriRegistryPath, unavailableImageDigest)

		releases = append(releases, mcfgv1alpha1.InternalReleaseImageBundleStatus{
			Name:  specRelease.Name,
			Image: imageRef,
			Conditions: []metav1.Condition{
				{
					Type:               string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
					Status:             metav1.ConditionFalse,
					Reason:             IRIStatusAPIIntNotAvailable,
					Message:            "The specified release image is not available",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
					Status:             metav1.ConditionTrue,
					Reason:             IRIStatusAPIIntNotAvailable,
					Message:            IRIStatusAPIIntNotAvailable,
					LastTransitionTime: metav1.Now(),
				},
			},
		})
	}
	return releases
}

// transformToAPIIntURL converts localhost:22625/path to api-int.<domain>:22625/path
func transformToAPIIntURL(localhostURL, clusterDomain string) string {
	return strings.Replace(localhostURL, "localhost", "api-int."+clusterDomain, 1)
}

// pingRegistry checks if the registry at the given URL is reachable.
func pingRegistry(registryURL string, caCert []byte) bool {
	// Extract host:port from the URL
	// registryURL is like "api-int.cluster.example.com:22625/openshift/release-images@sha256:..."
	parts := strings.SplitN(registryURL, "/", 2)
	if len(parts) == 0 {
		return false
	}
	baseURL := "https://" + parts[0] + "/v2/"

	// Create a CA cert pool with the provided CA certificate
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		klog.Warningf("Failed to parse CA certificate for registry ping")
		return false
	}

	client := &http.Client{
		Timeout: iriRegistryPingTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    caCertPool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	resp, err := client.Get(baseURL)
	if err != nil {
		klog.V(4).Infof("Registry ping failed for %s: %v", baseURL, err)
		return false
	}
	defer resp.Body.Close()

	// Registry /v2/ should return 200 or 401 (auth required) - both mean it's reachable
	reachable := resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
	klog.V(4).Infof("Registry ping to %s returned status %d, reachable: %v", baseURL, resp.StatusCode, reachable)
	return reachable
}

// updateDegradedCondition updates the Degraded condition to reflect cluster-level degradation
// while preserving the Available condition from healthy nodes
func updateDegradedCondition(conditions []metav1.Condition, iriStatus string, degradedNodes, notReadyNodes []string) []metav1.Condition {
	var reason, message string

	switch iriStatus {
	case IRIStatusSomeRegistriesUnavailable:
		reason = IRIStatusSomeRegistriesUnavailable
		message = fmt.Sprintf("The following nodes are degraded: [%s]. See the related MachineConfigNode resource status for more details.", strings.Join(degradedNodes, ", "))
	case IRIStatusSomeNodesNotAvailable:
		reason = IRIStatusSomeNodesNotAvailable
		message = fmt.Sprintf("The following nodes are not ready: [%s].", strings.Join(notReadyNodes, ", "))
	default:
		// Should not happen, but return original conditions
		return conditions
	}

	updatedConditions := make([]metav1.Condition, len(conditions))
	copy(updatedConditions, conditions)

	degradedCondition := metav1.Condition{
		Type:               string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	meta.SetStatusCondition(&updatedConditions, degradedCondition)

	return updatedConditions
}
