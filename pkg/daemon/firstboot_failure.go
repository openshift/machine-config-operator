package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"k8s.io/klog/v2"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const (
	// Sentinel file to prevent duplicate reports on retry loops
	firstbootFailureSentinelPath = "/run/mcd-firstboot-pivot-failed"

	// Timeout for HTTP request (short to avoid blocking retry path)
	firstbootReportTimeout = 10 * time.Second
)

// sendMCSFirstbootFailureReport sends a best-effort failure report to the MCS.
// Never returns an error - failures are logged only.
func (dn *Daemon) sendMCSFirstbootFailureReport(mc *mcfgv1.MachineConfig, imageURL string, pivotErr error) {
	// Check sentinel file - if it exists, we already reported this failure
	if _, err := os.Stat(firstbootFailureSentinelPath); err == nil {
		klog.V(2).Infof("Firstboot failure already reported (sentinel exists), skipping duplicate report")
		return
	}

	// Extract pool name from MachineConfig
	poolName := getPoolNameFromMachineConfig(mc)

	// Get node name - try daemon struct first, fallback to NODE_NAME env var, then hostname
	// During firstboot-complete-machineconfig, dn.name is not set because ClusterConnect hasn't run yet
	nodeID := dn.name
	if nodeID == "" {
		nodeID = os.Getenv("NODE_NAME")
	}
	if nodeID == "" {
		// Final fallback: use hostname (may be temporary name like "localhost")
		var err error
		nodeID, err = os.Hostname()
		if err != nil || nodeID == "" {
			klog.Warningf("Cannot send MCS failure report: unable to determine node name")
			return
		}
	}

	// Read MCS URL from node annotations file
	mcsURL, err := getMCSURLFromAnnotations()
	if err != nil {
		klog.Warningf("Cannot send MCS failure report: %v", err)
		return
	}

	// Build failure report payload
	report := ctrlcommon.FirstbootFailureReport{
		Pool:         poolName,
		NodeID:       nodeID,
		Stage:        "firstboot-update",
		ImageURL:     imageURL,
		ErrorMessage: pivotErr.Error(),
	}

	// Send report (fire-and-forget)
	if err := sendFailureReportHTTP(mcsURL, &report); err != nil {
		klog.Warning("Failed to send MCS failure report (best-effort)")
		return
	}

	// Create sentinel file to prevent duplicate reports
	if err := os.WriteFile(firstbootFailureSentinelPath, []byte(fmt.Sprintf("%s\n", time.Now().Format(time.RFC3339))), 0o644); err != nil {
		klog.Warningf("Failed to create firstboot failure sentinel file: %v", err)
	}

	klog.Infof("Sent firstboot failure report to MCS: pool=%s stage=firstboot-update", poolName)
}

// getPoolNameFromMachineConfig extracts the pool name from MachineConfig OwnerReferences.
// Falls back to "master" if OwnerReferences are not set.
func getPoolNameFromMachineConfig(mc *mcfgv1.MachineConfig) string {
	// Use OwnerReferences - the most reliable approach
	ownerMCPs := mc.GetOwnerReferences()
	if len(ownerMCPs) != 0 {
		return ownerMCPs[0].Name
	}

	// Fallback to master pool (OwnerRefs should always be set for rendered configs)
	klog.Warningf("Could not determine pool from MachineConfig %s (no OwnerReferences), using 'master' as fallback", mc.Name)
	return ctrlcommon.MachineConfigPoolMaster
}

// getMCSURLFromAnnotations reads the MCS URL from the node annotations file.
func getMCSURLFromAnnotations() (string, error) {
	// Read node annotations JSON file
	data, err := os.ReadFile(constants.InitialNodeAnnotationsFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read node annotations file: %w", err)
	}

	// Unmarshal annotations
	var annotations map[string]string
	if err := json.Unmarshal(data, &annotations); err != nil {
		return "", fmt.Errorf("failed to unmarshal node annotations: %w", err)
	}

	// Extract MCS URL
	mcsURL := annotations[constants.MachineConfigServerURLAnnotationKey]
	if mcsURL == "" {
		return "", fmt.Errorf("MCS URL not found in node annotations")
	}

	return mcsURL, nil
}

// sendFailureReportHTTP sends the failure report via HTTPS POST to the MCS.
func sendFailureReportHTTP(mcsBaseURL string, report *ctrlcommon.FirstbootFailureReport) error {
	endpoint := fmt.Sprintf("%s/v1/node-failure", strings.TrimSuffix(mcsBaseURL, "/"))

	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("failed to marshal failure report: %w", err)
	}

	tlsConfig, err := buildMCSTLSConfig()
	if err != nil {
		klog.Warningf("Failed to build MCS TLS config, falling back to system default: %v", err)
	}

	client := &http.Client{
		Timeout: firstbootReportTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), firstbootReportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// buildMCSTLSConfig loads the MCS root CA from disk and returns a tls.Config
// with a custom cert pool. Returns nil if the CA file does not exist, which
// falls back to the system default cert pool for backward compatibility.
func buildMCSTLSConfig() (*tls.Config, error) {
	caPEM, err := os.ReadFile(constants.MCSRootCABundlePath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(2).Infof("MCS CA bundle not found at %s, using system default cert pool", constants.MCSRootCABundlePath)
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read MCS CA bundle: %w", err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse any certificates from MCS CA bundle at %s", constants.MCSRootCABundlePath)
	}

	klog.V(2).Infof("Loaded MCS root CA from %s", constants.MCSRootCABundlePath)
	return &tls.Config{
		RootCAs:    certPool,
		MinVersion: tls.VersionTLS12,
	}, nil
}
