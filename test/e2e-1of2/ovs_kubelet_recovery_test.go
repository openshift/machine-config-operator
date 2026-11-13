package e2e_1of2_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
)

const recoveryService = "ovs-kubelet-recovery.service"
const requireRecoveryEnv = "MCO_REQUIRE_OVS_KUBELET_RECOVERY"

// TestOVSKubeletRecovery verifies the recovery unit against the node's actual
// systemd manager and OVS services. Run only when the tested MCO image has
// installed the OVN recovery unit.
func TestOVSKubeletRecovery(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.Background()

	apiGroups, err := cs.GetKubeclient().Discovery().ServerGroups()
	if err != nil {
		t.Fatalf("discover cluster API groups: %v", err)
	}
	hasConfigAPI := false
	for _, group := range apiGroups.Groups {
		if group.Name == "config.openshift.io" {
			hasConfigAPI = true
			break
		}
	}
	if !hasConfigAPI {
		t.Skip("OVS recovery e2e requires the OpenShift config API")
	}
	apiResources, err := cs.GetKubeclient().Discovery().ServerResourcesForGroupVersion("config.openshift.io/v1")
	if apierrors.IsNotFound(err) {
		t.Skip("OVS recovery e2e requires the OpenShift config API")
	}
	if err != nil {
		t.Fatalf("discover OpenShift config API resources: %v", err)
	}
	hasNetworkResource := false
	for _, resource := range apiResources.APIResources {
		if resource.Name == "networks" && resource.Kind == "Network" {
			hasNetworkResource = true
			break
		}
	}
	if !hasNetworkResource {
		t.Skip("OVS recovery e2e requires the OpenShift Network API")
	}

	network, err := cs.ConfigV1Interface.Networks().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if network.Spec.NetworkType != "OVNKubernetes" {
		t.Skipf("OVS kubelet recovery applies to OVNKubernetes; cluster network is %q", network.Spec.NetworkType)
	}

	workers, err := helpers.GetNodesByRole(cs, "worker")
	if err != nil {
		t.Fatal(err)
	}
	var readyWorkers []corev1.Node
	for _, worker := range workers {
		if _, hasMasterRole := worker.Labels["node-role.kubernetes.io/master"]; hasMasterRole {
			continue
		}
		if _, hasControlPlaneRole := worker.Labels["node-role.kubernetes.io/control-plane"]; hasControlPlaneRole {
			continue
		}
		if isNodeReady(worker) {
			readyWorkers = append(readyWorkers, worker)
		}
	}
	if len(readyWorkers) < 2 {
		t.Skipf("need two ready non-control-plane workers before disrupting one; found %d", len(readyWorkers))
	}
	// Compact clusters may label control-plane nodes as workers. Restrict this
	// disruptive test to nodes that carry neither control-plane role.
	node := readyWorkers[0]
	t.Logf("Selected worker node: %s", node.Name)

	loadState := runRecoveryNodeCommand(t, cs, node, "query recovery service load state", "chroot", "/rootfs", "systemctl", "show", recoveryService, "--property=LoadState", "--value")
	if strings.TrimSpace(loadState) != "loaded" {
		if os.Getenv(requireRecoveryEnv) == "true" {
			t.Fatalf("%s is not installed; candidate MCO image is required", recoveryService)
		}
		t.Skipf("%s is not installed; run against the test MCO image", recoveryService)
	}

	preflight := runRecoveryNodeCommand(t, cs, node, "recovery preflight", "chroot", "/rootfs", "/bin/bash", "-c", `
set -eu
test -x /usr/local/sbin/ovs-kubelet-recovery
test "$(systemctl is-enabled ovs-kubelet-recovery.service)" = static
systemctl cat kubelet-dependencies.target | grep -F 'OnFailure=ovs-kubelet-recovery.service'
for unit in ovsdb-server.service ovs-vswitchd.service openvswitch.service kubelet-dependencies.target crio.service kubelet.service; do
    systemctl is-active --quiet "$unit"
done
test "$(systemctl show ovs-configuration.service --property=Result --value)" = success
`)
	t.Logf("Recovery preflight passed on selected worker: %s", strings.TrimSpace(preflight))

	testID := string(uuid.NewUUID())
	triggerUnit := "ovs-kubelet-recovery-e2e-trigger-" + testID
	rescueUnit := "ovs-kubelet-recovery-e2e-rescue-" + testID
	dropinPath := "/run/systemd/system/ovsdb-server.service.d/90-ovs-kubelet-recovery-e2e-" + testID + ".conf"
	markerPath := "/run/ovs-kubelet-recovery-e2e-failed-" + testID
	rescueMarkerPath := "/run/ovs-kubelet-recovery-e2e-rescue-ran-" + testID

	beforeStart := strings.TrimSpace(runRecoveryNodeCommand(t, cs, node, "read recovery service start timestamp",
		"chroot", "/rootfs", "systemctl", "show", recoveryService,
		"--property=ExecMainStartTimestampMonotonic", "--value",
	))

	// Keep a node-side rescue timer armed if recovery fails and kubelet becomes
	// unavailable, so the MCD pod cannot be used to repair the test node.
	cleanupScript := fmt.Sprintf(`set +e
systemctl stop %q.timer %q.service %q.service >/dev/null 2>&1
systemctl stop %q >/dev/null 2>&1
rm -f %q %q %q
systemctl daemon-reload
systemctl reset-failed ovsdb-server.service ovs-vswitchd.service openvswitch.service ovs-configuration.service kubelet-dependencies.target crio.service kubelet.service %q >/dev/null 2>&1
systemctl start kubelet.service
`, rescueUnit, rescueUnit, triggerUnit, recoveryService, dropinPath, markerPath, rescueMarkerPath, recoveryService)
	t.Cleanup(func() {
		if _, err := helpers.ExecCmdOnNodeWithError(cs, node, "chroot", "/rootfs", "/bin/bash", "-c", cleanupScript); err != nil {
			t.Errorf("clean up recovery failure injection: %s", redactNodeDetails(node, err.Error()))
		}
	})

	installOverride := fmt.Sprintf(`set -eu
mkdir -p /run/systemd/system/ovsdb-server.service.d
cat > %q <<'EOF'
[Service]
ExecStartPre=/bin/sh -c "if [ ! -e %s ]; then /usr/bin/touch %s; echo injected-ovsdb-failure >&2; exit 1; fi"
EOF
systemctl daemon-reload
systemctl reset-failed ovsdb-server.service ovs-vswitchd.service openvswitch.service ovs-configuration.service kubelet-dependencies.target crio.service kubelet.service %s
`, dropinPath, markerPath, markerPath, recoveryService)
	runRecoveryNodeCommand(t, cs, node, "install OVSDB failure injection", "chroot", "/rootfs", "/bin/bash", "-c", installOverride)

	rescueScript := fmt.Sprintf(`set -eu
touch %q
rm -f %q
systemctl daemon-reload
systemctl reset-failed ovsdb-server.service ovs-vswitchd.service openvswitch.service ovs-configuration.service kubelet-dependencies.target crio.service kubelet.service %s || true
systemctl start kubelet.service
`, rescueMarkerPath, dropinPath, recoveryService)
	_, err = helpers.ExecCmdOnNodeWithError(cs, node,
		"chroot", "/rootfs", "systemd-run", "--unit="+rescueUnit,
		"--on-active=8m", "--collect", "--no-block", "/bin/bash", "-c", rescueScript,
	)
	if err != nil {
		t.Fatalf("schedule recovery safety timer: %s", redactNodeDetails(node, err.Error()))
	}

	triggerScript := `set -eu
systemctl stop kubelet.service crio.service kubelet-dependencies.target
systemctl stop ovsdb-server.service ovs-vswitchd.service openvswitch.service
sleep 5
systemctl start --no-block kubelet-dependencies.target
`
	_, err = helpers.ExecCmdOnNodeWithError(cs, node,
		"chroot", "/rootfs", "systemd-run", "--unit="+triggerUnit,
		"--collect", "--no-block", "/bin/bash", "-c", triggerScript,
	)
	if err != nil {
		t.Fatalf("start OVS failure injection: %s", redactNodeDetails(node, err.Error()))
	}
	t.Log("Triggered fail-once ovsdb-server start on selected worker; waiting for recovery")

	var rescued bool
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
		currentNode, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			if !retryableNodeGetError(err) {
				return false, err
			}
			t.Logf("worker readiness query failed; retrying: %s", redactNodeDetails(node, err.Error()))
			return false, nil
		}
		if !isNodeReady(*currentNode) {
			return false, nil
		}

		statusScript := fmt.Sprintf(`set -eu
if [ -e %q ]; then echo rescue-ran; exit 0; fi
start=$(systemctl show %s --property=ExecMainStartTimestampMonotonic --value)
state=$(systemctl show %s --property=ActiveState --value)
result=$(systemctl show %s --property=Result --value)
if [ "$start" != %q ] && [ "$state" = inactive ] && [ "$result" = success ]; then echo recovered; else echo waiting; fi
`, rescueMarkerPath, recoveryService, recoveryService, recoveryService, beforeStart)
		output, err := helpers.ExecCmdOnNodeWithError(cs, *currentNode, "chroot", "/rootfs", "/bin/bash", "-c", statusScript)
		if err != nil {
			t.Logf("worker is Ready but MCD exec is not available yet; retrying: %s", redactNodeDetails(*currentNode, err.Error()))
			return false, nil
		}
		switch strings.TrimSpace(redactNodeDetails(*currentNode, output)) {
		case "recovered":
			return true, nil
		case "rescue-ran":
			rescued = true
			return true, nil
		default:
			return false, nil
		}
	})
	if err != nil {
		t.Fatalf("worker did not recover through %s: %s", recoveryService, redactNodeDetails(node, err.Error()))
	}
	if rescued {
		t.Fatal("recovery did not complete before the node-side rescue timer ran")
	}

	verification := runRecoveryNodeCommand(t, cs, node, "verify OVS and kubelet recovery", "chroot", "/rootfs", "/bin/bash", "-c", fmt.Sprintf(`set -eu
for unit in ovsdb-server.service ovs-vswitchd.service openvswitch.service kubelet-dependencies.target crio.service kubelet.service; do
    systemctl is-active --quiet "$unit"
done
test "$(systemctl show ovs-configuration.service --property=Result --value)" = success
test -e %q
journalctl -b -u ovsdb-server.service --no-pager | grep -Fq injected-ovsdb-failure
journalctl -b -u kubelet-dependencies.target --no-pager | grep -Fq 'Triggering OnFailure='
journalctl -b -u %s --no-pager | grep -Fq 'OVS, dependency target, CRI-O and kubelet are active'
test ! -e %q
echo recovery-verified
`, markerPath, recoveryService, rescueMarkerPath))
	if strings.TrimSpace(verification) != "recovery-verified" {
		t.Fatalf("unexpected recovery verification output: %q", verification)
	}
	t.Log("Verified OVS failure, systemd OnFailure activation, recovery success, and active kubelet chain on selected worker")
}

func runRecoveryNodeCommand(t *testing.T, cs *framework.ClientSet, node corev1.Node, operation string, args ...string) string {
	t.Helper()
	output, err := helpers.ExecCmdOnNodeWithError(cs, node, args...)
	if err != nil {
		t.Fatalf("%s failed on selected worker: %s", operation, redactNodeDetails(node, err.Error()))
	}
	return redactNodeDetails(node, output)
}

func redactNodeDetails(node corev1.Node, value string) string {
	for _, address := range node.Status.Addresses {
		if address.Address != "" {
			value = strings.ReplaceAll(value, address.Address, "<node>")
		}
	}
	if node.Name != "" {
		value = strings.ReplaceAll(value, node.Name, "<node>")
	}
	return value
}

func retryableNodeGetError(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) || apierrors.IsUnexpectedServerError(err) {
		return true
	}
	var apiStatus apierrors.APIStatus
	if errors.As(err, &apiStatus) {
		code := apiStatus.Status().Code
		switch code {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, transientNetworkError := range []error{syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.ECONNABORTED, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.EPIPE} {
		if errors.Is(err, transientNetworkError) {
			return true
		}
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func TestRedactNodeDetails(t *testing.T) {
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-0.example.test"},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Address: "worker-0.example.test"}, {Address: "192.0.2.15"}}},
	}
	got := redactNodeDetails(node, "node=worker-0.example.test ip=192.0.2.15")
	if want := "node=<node> ip=<node>"; got != want {
		t.Fatalf("redacted node details = %q, want %q", got, want)
	}
}

func TestRetryableNodeGetError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "server timeout", err: apierrors.NewTimeoutError("request timed out", 0), want: true},
		{name: "too many requests", err: apierrors.NewTooManyRequests("rate limited", 1), want: true},
		{name: "server error", err: apierrors.NewInternalError(errors.New("internal error")), want: true},
		{name: "service unavailable", err: apierrors.NewServiceUnavailable("temporarily unavailable"), want: true},
		{name: "network timeout", err: temporaryNetworkError{}, want: true},
		{name: "connection reset", err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, want: true},
		{name: "end of stream", err: io.EOF, want: true},
		{name: "bad request", err: apierrors.NewBadRequest("invalid request")},
		{name: "not implemented", err: &apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusNotImplemented}}},
		{name: "generic error", err: errors.New("permanent error")},
		{name: "nil error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableNodeGetError(tc.err); got != tc.want {
				t.Fatalf("retryableNodeGetError() = %t, want %t", got, tc.want)
			}
		})
	}
}

type temporaryNetworkError struct{}

func (temporaryNetworkError) Error() string   { return "temporary network error" }
func (temporaryNetworkError) Timeout() bool   { return true }
func (temporaryNetworkError) Temporary() bool { return true }

func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
