package daemon

import (
	"bytes"
	"context"
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
func sendMCSFirstbootFailureReport(mc *mcfgv1.MachineConfig, imageURL string, pivotErr error) {
	// Check sentinel file - if it exists, we already reported this failure
	if _, err := os.Stat(firstbootFailureSentinelPath); err == nil {
		klog.V(2).Infof("Firstboot failure already reported (sentinel exists), skipping duplicate report")
		return
	}

	// Extract pool name from MachineConfig
	poolName := getPoolNameFromMachineConfig(mc)

	// The hostname is the only node identifier available here. The daemon's own
	// name is only populated by ClusterConnect/HypershiftConnect, neither of
	// which runs on the firstboot path, and NODE_NAME is injected via the
	// downward API into the MCD DaemonSet only. Note the hostname may still be a
	// provisioning-time placeholder such as "localhost" at this point.
	nodeID, err := os.Hostname()
	if err != nil || nodeID == "" {
		klog.Warningf("Cannot send MCS failure report: unable to determine node name")
		return
	}

	// Read MCS URL from the file written during ignition provisioning
	mcsURL, err := getMCSURL()
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

// getMCSURL reads the MCS base URL from the file written by the MCS during
// ignition provisioning.
func getMCSURL() (string, error) {
	data, err := os.ReadFile(constants.MCSURLFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read MCS URL file: %w", err)
	}

	mcsURL := strings.TrimSpace(string(data))
	if mcsURL == "" {
		return "", fmt.Errorf("MCS URL file %s is empty", constants.MCSURLFilePath)
	}

	return mcsURL, nil
}

// sendFailureReportHTTP sends the failure report via HTTPS POST to the MCS.
func sendFailureReportHTTP(mcsBaseURL string, report *ctrlcommon.FirstbootFailureReport) error {
	// Build endpoint URL
	endpoint := fmt.Sprintf("%s/v1/node-failure", strings.TrimSuffix(mcsBaseURL, "/"))

	// Marshal report to JSON
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("failed to marshal failure report: %w", err)
	}

	// Create HTTP client with timeout (uses default cert pool)
	client := &http.Client{
		Timeout: firstbootReportTimeout,
	}

	// Create request with context timeout
	ctx, cancel := context.WithTimeout(context.Background(), firstbootReportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response status (202 Accepted expected)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
