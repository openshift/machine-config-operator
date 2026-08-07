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
	k8sapiflag "k8s.io/component-base/cli/flag"
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

	// Read the MCS URL and TLS settings from the file written during ignition
	// provisioning
	connInfo, err := getMCSConnectionInfo(constants.FirstbootMCSConnectionInfoFilePath)
	if err != nil {
		klog.Warningf("Cannot send MCS failure report: %v", err)
		return
	}

	// The MCS serving cert is signed by a cluster-internal CA that is not in the
	// node's system trust store, so without the bundle the handshake cannot
	// succeed. Skip the report rather than attempt a send that is certain to fail.
	tlsConfig, err := buildMCSTLSConfig(constants.FirstbootMCSRootCABundlePath, connInfo.TLSMinVersion)
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
	if err := sendFailureReportHTTP(connInfo.URL, tlsConfig, &report); err != nil {
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

// getMCSConnectionInfo reads the MCS connection details from the file written
// by the MCS during ignition provisioning. The file is absent when the cluster
// has firstboot failure reporting disabled. The path is a parameter so the
// parsing can be exercised in tests.
func getMCSConnectionInfo(path string) (*ctrlcommon.MCSConnectionInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCS connection info file: %w", err)
	}

	connInfo := new(ctrlcommon.MCSConnectionInfo)
	if err := json.Unmarshal(data, connInfo); err != nil {
		return nil, fmt.Errorf("failed to parse MCS connection info file %s: %w", path, err)
	}

	if connInfo.URL == "" {
		return nil, fmt.Errorf("MCS connection info file %s has no URL", path)
	}

	return connInfo, nil
}

// sendFailureReportHTTP sends the failure report via HTTPS POST to the MCS.
func sendFailureReportHTTP(mcsBaseURL string, tlsConfig *tls.Config, report *ctrlcommon.FirstbootFailureReport) error {
	endpoint := fmt.Sprintf("%s/v1/node-failure", strings.TrimSuffix(mcsBaseURL, "/"))

	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("failed to marshal failure report: %w", err)
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
// pinned to it. There is no fallback to the system cert pool: the MCS serving
// cert is signed by a cluster-internal CA that is never added to the node's
// system trust store, so a handshake without this bundle cannot verify.
//
// tlsMinVersion is the cluster's security profile floor as served by the MCS,
// so a cluster that has raised its profile is not undercut by this client. The
// CA path is a parameter so the trust and version handling can be exercised in
// tests.
//
// G402 cannot prove the minimum version is high enough when it comes from a
// runtime value; k8sapiflag only ever returns TLS 1.0 or above, and the
// cluster's own API validation guards the value before it reaches us.
//
//nolint:gosec
func buildMCSTLSConfig(caPath, tlsMinVersion string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCS CA bundle at %s: %w", caPath, err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse any certificates from MCS CA bundle at %s", caPath)
	}

	// An empty value already resolves to TLS 1.2. Only an unrecognized one
	// errors, and that is not worth dropping the report over: fall back to the
	// same floor rather than lose the only signal this node can send.
	minVersion, err := k8sapiflag.TLSVersion(tlsMinVersion)
	if err != nil {
		klog.Warningf("Ignoring unusable TLS min version %q from MCS connection info, using VersionTLS12: %v", tlsMinVersion, err)
		minVersion = tls.VersionTLS12
	}

	klog.V(2).Infof("Loaded MCS root CA from %s", caPath)
	return &tls.Config{
		RootCAs:    certPool,
		MinVersion: minVersion,
	}, nil
}
