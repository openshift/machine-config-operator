package helpers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	aggerrs "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/yaml"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/davecgh/go-spew/spew"
	machineClientv1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	buildConstants "github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	rpmostreeclient "github.com/coreos/rpmostree-client-go/pkg/client"
	opv1 "github.com/openshift/api/operator/v1"
	mcoac "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
)

const (
	// Annotation which should be added to all objects created by an e2e test such as
	// ConfigMaps, MachineConfigs, MachineConfigPools, etc.
	UsedByE2ETestAnnoKey string = "machineconfiguration.openshift.io/used-by-e2e-test"

	// We have a separate label key so that we can do a label selector query. We
	// don't attach the test name to this label key because the test name may
	// contain invalid characters such as slashes or underscores.
	UsedByE2ETestLabelKey string = UsedByE2ETestAnnoKey

	machineAPINamespace string = "openshift-machine-api"
)

type CleanupFuncs struct {
	funcs []func()
}

func (c *CleanupFuncs) Add(f func()) {
	c.funcs = append(c.funcs, f)
}

func (c *CleanupFuncs) Run() {
	for _, f := range c.funcs {
		f()
	}
}

func NewCleanupFuncs() CleanupFuncs {
	return CleanupFuncs{
		funcs: []func(){},
	}
}

func ApplyMC(t *testing.T, cs *framework.ClientSet, mc *mcfgv1.MachineConfig) func() {
	SetMetadataOnObject(t, mc)

	_, err := cs.MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	require.Nil(t, err)

	return func() {
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}))
	}
}

// Applies a MachineConfig to a given MachineConfigPool, if a MachineConfig is
// provided. If a MachineConfig is not provided (i.e., nil), it will skip the
// apply process and wait for the MachineConfigPool to include the "00-worker"
// MachineConfig. Returns a delete function that will no-op if a MachineConfig
// is not provided.
func maybeApplyMCAndWaitForPoolComplete(t *testing.T, cs *framework.ClientSet, poolName string, mc *mcfgv1.MachineConfig) func() {
	if mc != nil {
		deleteFunc := ApplyMC(t, cs, mc)
		WaitForConfigAndPoolComplete(t, cs, poolName, mc.Name)
		return func() {
			t.Logf("Deleting MachineConfig %s", mc.Name)
			deleteFunc()
		}
	}

	mcName := "00-worker"
	t.Logf("No MachineConfig provided, will wait for pool %q to include MachineConfig %q", poolName, mcName)
	WaitForConfigAndPoolComplete(t, cs, poolName, mcName)
	return func() {}
}

// Creates a pool, adds the provided node to it, applies the MachineConfig (if
// provided), waits for the rollout. Returns an idempotent function which can
// be used both for reverting and cleanup purposes.
func CreatePoolAndApplyMCToNode(t *testing.T, cs *framework.ClientSet, poolName string, node corev1.Node, mc *mcfgv1.MachineConfig) func() {
	workerMCPName := "worker"

	t.Logf("Setting up pool %s", poolName)

	unlabelFunc := LabelNode(t, cs, node, MCPNameToRole(poolName))
	deleteMCPFunc := CreateMCP(t, cs, poolName)

	t.Logf("Target Node: %s", node.Name)

	mcDeleteFunc := maybeApplyMCAndWaitForPoolComplete(t, cs, poolName, mc)

	mcpMCName := GetMcName(t, cs, poolName)
	require.Nil(t, WaitForNodeConfigChange(t, cs, node, mcpMCName))

	return MakeIdempotent(func() {
		t.Logf("Cleaning up MCP %s", poolName)
		t.Logf("Removing label %s from node %s", MCPNameToRole(poolName), node.Name)
		unlabelFunc()

		workerMC := GetMcName(t, cs, workerMCPName)

		// Wait for the worker pool to catch up with the deleted label
		time.Sleep(5 * time.Second)

		t.Logf("Waiting for %s pool to finish applying %s", workerMCPName, workerMC)
		require.Nil(t, WaitForPoolComplete(t, cs, workerMCPName, workerMC))

		t.Logf("Ensuring node has %s pool config before deleting pool %s", workerMCPName, poolName)
		require.Nil(t, WaitForNodeConfigChange(t, cs, node, workerMC))

		t.Logf("Deleting MCP %s", poolName)
		deleteMCPFunc()

		mcDeleteFunc()
	})
}

// Creates a MachineConfigPool, waits for the MachineConfigPool to inherit the
// worker MachineConfig "00-worker", and assigns a node to it. Returns an
// idempotent function which can be used for both reverting and cleanup
// purposes.
func CreatePoolWithNode(t *testing.T, cs *framework.ClientSet, poolName string, node corev1.Node) func() {
	return CreatePoolAndApplyMCToNode(t, cs, poolName, node, nil)
}

// Selects a random node from the "worker" pool, creates a MachineConfigPool,
// and applies a MachineConfig to it. Returns an idempotent function which can
// be used for both reverting and cleanup purposes.
func CreatePoolAndApplyMC(t *testing.T, cs *framework.ClientSet, poolName string, mc *mcfgv1.MachineConfig) func() {
	node := GetRandomNode(t, cs, "worker")
	return CreatePoolAndApplyMCToNode(t, cs, poolName, node, mc)
}

// GetMcName returns the current configuration name of the machine config pool poolName
func GetMcName(t *testing.T, cs *framework.ClientSet, poolName string) string {
	// grab the initial machineconfig used by the worker pool
	// this MC is gonna be the one which is going to be reapplied once the previous MC is deleted
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	require.Nil(t, err)
	return mcp.Status.Configuration.Name
}

// WaitForConfigAndPoolComplete is a helper function that gets a renderedConfig and waits for its pool to complete.
// The return value is the final rendered config.
func WaitForConfigAndPoolComplete(t *testing.T, cs *framework.ClientSet, pool, mcName string) string {
	config, err := WaitForRenderedConfig(t, cs, pool, mcName)
	require.Nil(t, err, "failed to render machine config %s from pool %s", mcName, pool)
	err = WaitForPoolComplete(t, cs, pool, config)
	require.Nil(t, err, "pool %s did not update to config %s", pool, config)
	return config
}

// WaitForRenderedConfig polls a MachineConfigPool until it has
// included the given mcName in its config, and returns the new
// rendered config name.
func WaitForRenderedConfig(t *testing.T, cs *framework.ClientSet, pool, mcName string) (string, error) {
	return WaitForRenderedConfigs(t, cs, pool, mcName)
}

// WaitForRenderedConfigs polls a MachineConfigPool until it has
// included the given mcNames in its config, and returns the new
// rendered config name.
func WaitForRenderedConfigs(t *testing.T, cs *framework.ClientSet, pool string, mcNames ...string) (string, error) {
	var renderedConfig string
	startTime := time.Now()
	found := make(map[string]bool)

	ctx := context.Background()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Set up the list
		for _, name := range mcNames {
			found[name] = false
		}

		// Update found based on the MCP
		mcp, err := cs.MachineConfigPools().Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcp.Spec.Configuration.Source {
			if _, ok := found[mc.Name]; ok {
				found[mc.Name] = true
			}
		}

		// If any are still false, then they weren't included in the MCP
		for _, nameFound := range found {
			if !nameFound {
				return false, nil
			}
		}

		// All the required names were found
		renderedConfig = mcp.Spec.Configuration.Name
		return true, nil
	}); err != nil {
		return "", fmt.Errorf("machine configs %v hasn't been picked by pool %s (waited %s): %w", notFoundNames(found), pool, time.Since(startTime), err)
	}
	t.Logf("Pool %s has rendered configs %v with %s (waited %v)", pool, mcNames, renderedConfig, time.Since(startTime))
	return renderedConfig, nil
}

func notFoundNames(foundNames map[string]bool) []string {
	out := []string{}
	for name, found := range foundNames {
		if !found {
			out = append(out, name)
		}
	}
	return out
}

// WaitForPoolComplete polls a pool until it has completed an update to target
func WaitForPoolComplete(t *testing.T, cs *framework.ClientSet, pool, target string) error {
	startTime := time.Now()
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 25*time.Minute, false, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcp.Status.Configuration.Name != target {
			return false, nil
		}
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("pool %s didn't report %s to updated (waited %s): %w", pool, target, time.Since(startTime), err)
	}
	t.Logf("Pool %s has completed %s (waited %v)", pool, target, time.Since(startTime))
	return nil
}

// WaitForOneMasterNode waits until atleast one master node has completed an update
func WaitForOneMasterNodeToBeReady(t *testing.T, cs *framework.ClientSet) error {
	startTime := time.Now()
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 12*time.Minute, false, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, "master", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		// Check if the pool has atleast one updated node(mid-upgrade), or if the pool has completed the upgrade to the new config(the additional check for spec==status here is
		// to ensure we are not checking an older "Updated" condition and the MCP fields haven't caught up yet
		if (apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) && mcp.Status.UpdatedMachineCount > 0) ||
			(apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) && (mcp.Spec.Configuration.Name == mcp.Status.Configuration.Name)) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("the master pool has failed to update a node in time (waited %s): %w", time.Since(startTime), err)
	}
	t.Logf("The master pool has atleast one updated node (waited %v)", time.Since(startTime))
	return nil
}

// Waits for both the node image and config to change.
func WaitForNodeConfigAndImageChange(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcName, image string) error {
	startTime := time.Now()
	ctx := context.TODO()

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 20*time.Minute, true, func(ctx context.Context) (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return hasNodeConfigChanged(*n, mcName) && hasNodeImageChanged(*n, image) && isMCDDone(*n), nil
	})

	if err != nil {
		return fmt.Errorf("node config / image change did not occur (waited %v): %w", time.Since(startTime), err)
	}

	t.Logf("Node %s changed config to %s and changed image to %s (waited %v)", node.Name, mcName, image, time.Since(startTime))
	return nil
}

// Waits for a node image to change.
func WaitForNodeImageChange(t *testing.T, cs *framework.ClientSet, node corev1.Node, image string) error {
	startTime := time.Now()
	ctx := context.TODO()

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 20*time.Minute, true, func(ctx context.Context) (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return hasNodeImageChanged(*n, image) && isMCDDone(*n), nil
	})

	if err != nil {
		return fmt.Errorf("node image change did not occur (waited %v): %w", time.Since(startTime), err)
	}

	t.Logf("Node %s changed image to %s (waited %v)", node.Name, image, time.Since(startTime))
	return nil
}

// Waits for a node config to change.
func WaitForNodeConfigChange(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcName string) error {
	startTime := time.Now()
	ctx := context.TODO()

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 20*time.Minute, true, func(ctx context.Context) (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return hasNodeConfigChanged(*n, mcName) && isMCDDone(*n), nil
	})

	if err != nil {
		return fmt.Errorf("node config change did not occur (waited %v): %w", time.Since(startTime), err)
	}

	t.Logf("Node %s changed config to %s (waited %v)", node.Name, mcName, time.Since(startTime))
	return nil
}

func WaitForNodeReady(t *testing.T, cs *framework.ClientSet, node corev1.Node) error {
	startTime := time.Now()
	ctx := context.TODO()

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 30*time.Minute, true, func(ctx context.Context) (bool, error) {
		time.Sleep(10 * time.Second)

		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		nodeReady := false
		for _, condition := range n.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				nodeReady = true
			}
		}

		if nodeReady {
			return true, nil
		}

		return false, nil
	})

	if err != nil {
		return fmt.Errorf("node %s did not become ready (waited %v): %w", node.Name, time.Since(startTime), err)
	}

	t.Logf("Node %s is now ready (waited %v)", node.Name, time.Since(startTime))
	return nil
}

// WaitForPoolComplete polls a pool until it has completed any update
func WaitForPoolCompleteAny(t *testing.T, cs *framework.ClientSet, pool string) error {
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		// wait until the cluter is back to normal (for the next test at least)
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("Machine config pool did not complete an update: %v", err)
	}
	return nil
}

// WaitForPausedConfig waits for configuration to be pending in a paused pool
func WaitForPausedConfig(t *testing.T, cs *framework.ClientSet, pool string) error {
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		// paused == not updated, not updating, not degraded
		if apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) &&
			apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) &&
			apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("Machine config pool never entered the state where configuration was waiting behind pause: %v", err)
	}
	return nil
}

// WaitForPodStart waits for a pod with the given name prefix in the given namespace to start.
func WaitForPodStart(cs *framework.ClientSet, podPrefix, namespace string) error {
	ctx := context.TODO()

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		podList, err := cs.CoreV1Interface.Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, pod := range podList.Items {
			if strings.HasPrefix(pod.Name, podPrefix) {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
			}
		}
		return false, nil
	})
}

// WaitForPodStop waits for a pod with the given name prefix in the given namespace to stop (i.e., to be deleted).
func WaitForPodStop(cs *framework.ClientSet, podPrefix, namespace string) error {
	ctx := context.TODO()

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		podList, err := cs.CoreV1Interface.Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, pod := range podList.Items {
			if strings.HasPrefix(pod.Name, podPrefix) {
				// Pod with prefix still exists, so we return false
				return false, nil
			}
		}
		// If we reached here, it means no pod with the given prefix exists, so we return true
		return true, nil
	})
}

// CheckDeploymentExists checks if a deployment with the given name in the given namespace exists.
func CheckDeploymentExists(cs *framework.ClientSet, name, namespace string) (bool, error) {
	ctx := context.TODO()
	_, err := cs.Deployments(namespace).Get(ctx, name, metav1.GetOptions{})

	if err == nil {
		return true, nil
	}

	if errors.IsNotFound(err) {
		return false, nil
	}

	return false, err
}

// GetMonitoringToken retrieves the token from the openshift-monitoring secrets in the prometheus-k8s namespace.
// It is equivalent to "oc sa get-token prometheus-k8s -n openshift-monitoring"
func GetMonitoringToken(_ *testing.T, cs *framework.ClientSet) (string, error) {
	token, err := cs.
		ServiceAccounts("openshift-monitoring").
		CreateToken(
			context.TODO(),
			"prometheus-k8s",
			&authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{},
			},
			metav1.CreateOptions{},
		)
	if err != nil {
		return "", fmt.Errorf("Could not request openshift-monitoring token")
	}
	return token.Status.Token, nil
}

// WaitForCertStatusToChange queries a controllerconfig until the ControllerCertificates changes.
func WaitForCertStatusToChange(t *testing.T, cs *framework.ClientSet, oldData string) error {
	ctx := context.TODO()
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(_ context.Context) (bool, error) {
		controllerConfig, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
		require.Nil(t, err)

		// shows us that the status is updating

		for _, cert := range controllerConfig.Status.ControllerCertificates {
			t.Logf("comparing: %s to %s\n", oldData, cert.Subject)
			t.Logf("also, other data: %s %s\n", cert.BundleFile, cert.Subject)
			if cert.BundleFile == "KubeAPIServerServingCAData" {
				if oldData != cert.Subject {
					return true, nil
				}
			}
		}

		return false, nil
	}); err != nil {
		return err
	}

	return nil
}

func WaitForCADataToAppear(t *testing.T, cs *framework.ClientSet) error {
	err := wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, 2*time.Minute, true, func(_ context.Context) (bool, error) {
		controllerConfig, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
		require.Nil(t, err)
		nodes, err := GetNodesByRole(cs, "worker")
		require.Nil(t, err)
		for _, cert := range controllerConfig.Spec.ImageRegistryBundleUserData {
			foundCA, _ := ExecCmdOnNodeWithError(cs, nodes[0], "ls", canonicalizeNodeFilePath(filepath.Join("/etc/docker/certs.d", cert.File)))
			if strings.Contains(foundCA, "ca.crt") {
				return true, nil
			}
		}
		return false, nil
	})
	return err
}

// WaitForMCDToSyncCert waits for the MCD to write annotation on the latest controllerconfig resourceVersion,
// to indicate that is has completed the certificate write
func WaitForMCDToSyncCert(t *testing.T, cs *framework.ClientSet, node corev1.Node, resourceVersion string) error {
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 4*time.Minute, true, func(ctx context.Context) (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if n.Annotations[constants.ControllerConfigResourceVersionKey] == resourceVersion {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("Machine config daemon never wrote updated controllerconfig resourceVersion annotation: %v", err)
	}
	return nil
}

// ForceKubeApiserverCertificateRotation sets the kube-apiserver-to-kubelet-signer's not-after date to nil, which causes the
// apiserver to rotate it
func ForceKubeApiserverCertificateRotation(cs *framework.ClientSet) error {
	// Take note that the slash had to be encoded as ~1 because it's a reference: https://www.rfc-editor.org/rfc/rfc6901#section-3
	certPatch := fmt.Sprintf(`[{"op":"replace","path":"/metadata/annotations/auth.openshift.io~1certificate-not-after","value": null }]`)
	_, err := cs.Secrets("openshift-kube-apiserver-operator").Patch(context.TODO(), "kube-apiserver-to-kubelet-signer", types.JSONPatchType, []byte(certPatch), metav1.PatchOptions{})
	return err
}

// ForceImageRegistryCertRotationCertificateRotation sets the imae registry cert's not-after date to nil, which causes the
// apiserver to rotate it
func ForceImageRegistryCertRotationCertificateRotation(cs *framework.ClientSet) error {
	cfg, err := cs.ConfigV1Interface.Images().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		return err
	}
	// Take note that the slash had to be encoded as ~1 because it's a reference: https://www.rfc-editor.org/rfc/rfc6901#section-3
	certPatch := fmt.Sprintf(`[{"op":"replace","path":"/metadata/annotations/auth.openshift.io~1certificate-not-after","value": null }]`)
	_, err = cs.Secrets("openshift-config").Patch(context.TODO(), cfg.Spec.AdditionalTrustedCA.Name, types.JSONPatchType, []byte(certPatch), metav1.PatchOptions{})
	return err
}

// GetKubeletCABundleFromConfigmap fetches the latest kubelet ca bundle data from
func GetKubeletCABundleFromConfigmap(cs *framework.ClientSet) (string, error) {
	certBundle, err := cs.ConfigMaps("openshift-config-managed").Get(context.TODO(), "kube-apiserver-client-ca", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("Could not get in-cluster kube-apiserver-client-ca configmap")
	}
	if cert, ok := certBundle.Data["ca-bundle.crt"]; ok {
		return cert, nil
	}
	return "", fmt.Errorf("Could not find ca-bundle")
}

// Applies a given label to a node and returns an unlabel function.
func LabelNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, label string) func() {
	ctx := context.Background()

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		n.Labels[label] = ""
		SetMetadataOnObject(t, n)
		_, err = cs.CoreV1Interface.Nodes().Update(ctx, n, metav1.UpdateOptions{})
		return err
	})

	require.Nil(t, err, "unable to label %s node %s with infra: %s", label, node.Name, err)

	t.Logf("Applied label %q to node %s", label, node.Name)

	return MakeIdempotent(func() {

		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			n, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			delete(n.Labels, label)
			delete(n.Labels, UsedByE2ETestLabelKey)
			delete(n.Annotations, UsedByE2ETestAnnoKey)
			_, err = cs.CoreV1Interface.Nodes().Update(ctx, n, metav1.UpdateOptions{})
			return err
		})
		require.Nil(t, err, "unable to remove label %q from node %q: %s", label, node.Name, err)
		t.Logf("Removed label %q from node %s", label, node.Name)
	})
}

// Gets a random node from a given pool. Checks for whether the node is ready
// and if no nodes are ready, it will poll for up to 5 minutes for a node to
// become available.
func GetRandomNode(t *testing.T, cs *framework.ClientSet, pool string) corev1.Node {
	if node := getRandomNode(t, cs, pool); isNodeReady(node) {
		return node
	}

	waitPeriod := time.Minute * 5

	t.Logf("No ready nodes found for pool %s, waiting up to %s for a ready node to become available", pool, waitPeriod)

	var targetNode corev1.Node

	err := wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, waitPeriod, true, func(_ context.Context) (bool, error) {
		if node := getRandomNode(t, cs, pool); isNodeReady(node) {
			targetNode = node
			return true, nil
		}

		return false, nil
	})

	require.NoError(t, err, "no ready nodes found in pool %s after %s", pool, waitPeriod)
	return targetNode
}

// Gets a random node from a given pool.
func getRandomNode(t *testing.T, cs *framework.ClientSet, pool string) corev1.Node {
	nodes, err := GetNodesByRole(cs, pool)
	require.Nil(t, err)
	require.NotEmpty(t, nodes)

	// Disable gosec here to avoid throwing
	// G404: Use of weak random number generator (math/rand instead of crypto/rand)
	// #nosec
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	return nodes[rnd.Intn(len(nodes))]
}

// LabelRandomNodeFromPool gets all nodes in pool and chooses one at random to label
func LabelRandomNodeFromPool(t *testing.T, cs *framework.ClientSet, pool, label string) func() {
	infraNode := GetRandomNode(t, cs, pool)
	return LabelNode(t, cs, infraNode, label)
}

// GetSingleNodeByRoll gets all nodes by role pool, and asserts there should only be one
func GetSingleNodeByRole(t *testing.T, cs *framework.ClientSet, role string) corev1.Node {
	nodes, err := GetNodesByRole(cs, role)
	require.Nil(t, err)
	require.Len(t, nodes, 1)
	return nodes[0]
}

// GetNodesByRole gets all nodes labeled with role role
func GetNodesByRole(cs *framework.ClientSet, role string) ([]corev1.Node, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{fmt.Sprintf("node-role.kubernetes.io/%s", role): ""}).String(),
	}
	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	return nodes.Items, nil
}

// CreateMCP create a machine config pool with name mcpName
// it will also use mcpName as the label selector, so any node you want to be included
// in the pool should have a label node-role.kubernetes.io/mcpName = ""
func CreateMCP(t *testing.T, cs *framework.ClientSet, mcpName string) func() {
	infraMCP := &mcfgv1.MachineConfigPool{}
	infraMCP.Name = mcpName
	nodeSelector := metav1.LabelSelector{}
	infraMCP.Spec.NodeSelector = &nodeSelector
	infraMCP.Spec.NodeSelector.MatchLabels = make(map[string]string)
	infraMCP.Spec.NodeSelector.MatchLabels[MCPNameToRole(mcpName)] = ""
	mcSelector := metav1.LabelSelector{}
	infraMCP.Spec.MachineConfigSelector = &mcSelector
	infraMCP.Spec.MachineConfigSelector.MatchExpressions = []metav1.LabelSelectorRequirement{
		{
			Key:      mcfgv1.MachineConfigRoleLabelKey,
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"worker", mcpName},
		},
	}
	infraMCP.ObjectMeta.Labels = make(map[string]string)
	infraMCP.ObjectMeta.Labels[mcpName] = ""

	SetMetadataOnObject(t, infraMCP)

	_, err := cs.MachineConfigPools().Create(context.TODO(), infraMCP, metav1.CreateOptions{})
	switch {
	case err == nil:
		t.Logf("Created MachineConfigPool %q", mcpName)
	case errors.IsAlreadyExists(err):
		t.Logf("Reusing existing MachineConfigPool %q", mcpName)
	default:
		t.Fatalf("Could not create MachineConfigPool %q: %s", mcpName, err)
	}

	return func() {
		err := cs.MachineConfigPools().Delete(context.TODO(), mcpName, metav1.DeleteOptions{})
		require.Nil(t, err)
		t.Logf("Deleted MachineConfigPool %q", mcpName)
	}
}

type SSHPaths struct {
	// The path where SSH keys are expected to be found.
	Expected string
	// The path where SSH keys are *not* expected to be found.
	NotExpected string
}

// Determines where to expect SSH keys for the core user on a given node based upon the node's OS.
func GetSSHPaths(os osrelease.OperatingSystem) SSHPaths {
	if os.IsEL9() || os.IsSCOS() || os.IsFCOS() {
		return SSHPaths{
			Expected:    constants.RHCOS9SSHKeyPath,
			NotExpected: constants.RHCOS8SSHKeyPath,
		}
	}

	return SSHPaths{
		Expected:    constants.RHCOS8SSHKeyPath,
		NotExpected: constants.RHCOS9SSHKeyPath,
	}
}

// MCPNameToRole converts a mcpName to a node role label
func MCPNameToRole(mcpName string) string {
	return fmt.Sprintf("node-role.kubernetes.io/%s", mcpName)
}

// CreateMC creates a machine config object with name and role
func CreateMC(name, role string) *mcfgv1.MachineConfig {
	return NewMachineConfig(name, MCLabelForRole(role), "", nil)
}

// Asserts that a given file is present on the underlying node.
func AssertFileOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	t.Helper()

	path = canonicalizeNodeFilePath(path)

	out, err := ExecCmdOnNodeWithError(cs, node, "stat", path)

	return assert.NoError(t, err, "expected to find file %s on %s, got:\n%s\nError: %s", path, node.Name, out, err)
}

// Asserts that a given file is *not* present on the underlying node.
func AssertFileNotOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	t.Helper()

	path = canonicalizeNodeFilePath(path)

	out, err := ExecCmdOnNodeWithError(cs, node, "stat", path)

	return assert.Error(t, err, "expected not to find file %s on %s, got:\n%s", path, node.Name, out) &&
		assert.Contains(t, out, "No such file or directory", "expected command output to contain 'No such file or directory', got: %s", out)
}

// Asserts that a given file exists on the underlying node and returns the content of the file.
func GetFileContentOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) string {
	t.Helper()

	path = canonicalizeNodeFilePath(path)

	out, err := ExecCmdOnNodeWithError(cs, node, "cat", path)
	assert.NoError(t, err, "expected to find file %s on %s, got:\n%s", path, node.Name, out)
	return out
}

// Adds the /rootfs onto a given file path, if not already present.
func canonicalizeNodeFilePath(path string) string {
	rootfs := "/rootfs"

	if !strings.HasPrefix(path, rootfs) {
		return filepath.Join(rootfs, path)
	}

	return path
}

// ExecCmdOnNode finds a node's mcd, and oc rsh's into it to execute a command on the node
// all commands should use /rootfs as root
func ExecCmdOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, subArgs ...string) string {
	t.Helper()

	cmd, err := execCmdOnNode(cs, node, subArgs...)
	require.Nil(t, err, "could not prepare to exec cmd %v on node %s: %s", subArgs, node.Name, err)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		// common err is that the mcd went down mid cmd. Re-try for good measure
		cmd, err = execCmdOnNode(cs, node, subArgs...)
		require.Nil(t, err, "could not prepare to exec cmd %v on node %s: %s", subArgs, node.Name, err)
		out, err = cmd.Output()

	}
	require.Nil(t, err, "failed to exec cmd %v on node %s: %s", subArgs, node.Name, string(out))
	return string(out)
}

// Gets the rpm-ostree status for a given node.
func GetRPMOStreeStatusForNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) *rpmostreeclient.Status {
	status, err := getRPMOStreeStatusForNode(cs, node)
	require.NoError(t, err)

	return status
}

// Internal-only version of GetRPMOStreeStatusForNode()
func getRPMOStreeStatusForNode(cs *framework.ClientSet, node corev1.Node) (*rpmostreeclient.Status, error) {
	cmd, err := execCmdOnNode(cs, node, "chroot", "/rootfs", "rpm-ostree", "status", "--json")
	if err != nil {
		return nil, fmt.Errorf("could not construct command: %w", err)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not execute command %s on node %s: %w", cmd.String(), node.Name, err)
	}

	status := &rpmostreeclient.Status{}

	if err := json.Unmarshal(output, status); err != nil {
		return nil, fmt.Errorf("could not parse rpm-ostree status for node %s: %w", node.Name, err)
	}

	return status, nil
}

// Gets the currently booted rpmostree deployment for a given node.
func getBootedRPMOstreeDeployment(cs *framework.ClientSet, node corev1.Node) (*rpmostreeclient.Deployment, error) {
	status, err := getRPMOStreeStatusForNode(cs, node)
	if err != nil {
		return nil, fmt.Errorf("could not get rpm-ostree status for node %s: %w", node.Name, err)
	}

	for _, deployment := range status.Deployments {
		deployment := deployment
		if deployment.Booted {
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("no booted deployments found on node %s", node.Name)
}

// Asserts that a given node is booted into a specific image. This works by
// examining the rpm-ostree status and determining if the currently booted
// deployment is the expected image.
func AssertNodeBootedIntoImage(t *testing.T, cs *framework.ClientSet, node corev1.Node, expectedImagePullspec string) bool {
	deployment, err := getBootedRPMOstreeDeployment(cs, node)
	require.NoError(t, err)

	return assert.Contains(t, deployment.ContainerImageReference, expectedImagePullspec, "expected node %q to be booted into image %q, got %q", node.Name, expectedImagePullspec, deployment.ContainerImageReference)
}

// Asserts that a given node is not booted into a specific image. This works by
// examining the rpm-ostree status and determining if the currently booted
// deployment is the expected image.
func AssertNodeNotBootedIntoImage(t *testing.T, cs *framework.ClientSet, node corev1.Node, unexpectedImagePullspec string) bool {
	deployment, err := getBootedRPMOstreeDeployment(cs, node)
	require.NoError(t, err)

	return assert.NotContains(t, deployment.ContainerImageReference, unexpectedImagePullspec, "expected node %q not to be booted into %q", node.Name, unexpectedImagePullspec)
}

// ExecCmdOnNodeWithError behaves like ExecCmdOnNode, with the exception that
// any errors are returned to the caller for inspection. This allows one to
// execute a command that is expected to fail; e.g., stat /nonexistant/file.
func ExecCmdOnNodeWithError(cs *framework.ClientSet, node corev1.Node, subArgs ...string) (string, error) {
	cmd, err := execCmdOnNode(cs, node, subArgs...)
	if err != nil {
		return "", err
	}

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ExecCmdOnNode finds a node's mcd, and oc rsh's into it to execute a command on the node
// all commands should use /rootfs as root
func execCmdOnNode(cs *framework.ClientSet, node corev1.Node, subArgs ...string) (*exec.Cmd, error) {
	mcd, err := mcdForNode(cs, &node)
	if err != nil {
		return nil, fmt.Errorf("could not get MCD for node %s: %w", node.Name, err)
	}

	mcdName := mcd.ObjectMeta.Name

	args := []string{"rsh",
		"-n", "openshift-machine-config-operator",
		"-c", "machine-config-daemon",
		mcdName}
	args = append(args, subArgs...)

	return NewOcCommand(cs, args...)
}

// NewOcCommand() configures an exec.Cmd instance to pass the provided
// arguments into the oc command. Returns an error if the oc command is missing
// or the KUBECONFIG is also missing.
func NewOcCommand(cs *framework.ClientSet, args ...string) (*exec.Cmd, error) {
	// Check for an oc binary in $PATH.
	path, err := exec.LookPath("oc")
	if err != nil {
		return nil, fmt.Errorf("could not locate oc command: %w", err)
	}

	// Get the kubeconfig file path
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return nil, fmt.Errorf("could not get kubeconfig: %w", err)
	}

	cmd := exec.Command(path, args...)
	// If one passes a path to a kubeconfig via NewClientSet instead of setting
	// $KUBECONFIG, oc will be unaware of it. To remedy, we explicitly set
	// KUBECONFIG to the value held by the clientset.
	cmd.Env = append(cmd.Env, "KUBECONFIG="+kubeconfig)

	return cmd, nil
}

// NewOcCommandWithOutput() performs the same as above while also wiring
// cmd.Stdout to os.Stdout and cmd.Stderr to os.Stderr.
func NewOcCommandWithOutput(cs *framework.ClientSet, args ...string) (*exec.Cmd, error) {
	cmd, err := NewOcCommand(cs, args...)
	if err != nil {
		return nil, err
	}

	cmd.Stdout = os.Stdout
	cmd.Stdout = os.Stderr

	return cmd, nil
}

// IsOKDCluster checks whether the Upstream field on the CV spec references an OKD release controller
func IsOKDCluster(cs *framework.ClientSet) (bool, error) {
	cv, err := cs.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	// TODO: Adjust this as OKD becomes available for different platforms, e.g., arm64.
	okdReleaseControllers := sets.NewString("https://amd64.origin.releases.ci.openshift.org/graph")
	return okdReleaseControllers.Has(string(cv.Spec.Upstream)), nil
}

func MCLabelForRole(role string) map[string]string {
	mcLabels := make(map[string]string)
	mcLabels[mcfgv1.MachineConfigRoleLabelKey] = role
	return mcLabels
}

func MCLabelForWorkers() map[string]string {
	return MCLabelForRole("worker")
}

// TODO consider also testing for Ign2
// func createIgn2File(path, content, fs string, mode int) ign2types.File {
// 	return ign2types.File{
// 		FileEmbedded1: ign2types.FileEmbedded1{
// 			Contents: ign2types.FileContents{
// 				Source: content,
// 			},
// 			Mode: &mode,
// 		},
// 		Node: ign2types.Node{
// 			Filesystem: fs,
// 			Path:       path,
// 			User: &ign2types.NodeUser{
// 				Name: "root",
// 			},
// 		},
// 	}
// }

// Creates an Ign3 file whose contents are gzipped and encoded according to
// https://datatracker.ietf.org/doc/html/rfc2397
func CreateGzippedIgn3File(path, content string, mode int) (ign3types.File, error) {
	ign3File := ign3types.File{}

	buf := bytes.NewBuffer([]byte{})

	gzipWriter := gzip.NewWriter(buf)
	if _, err := gzipWriter.Write([]byte(content)); err != nil {
		return ign3File, err
	}

	if err := gzipWriter.Close(); err != nil {
		return ign3File, err
	}

	ign3File = CreateEncodedIgn3File(path, buf.String(), mode)
	ign3File.Contents.Compression = StrToPtr("gzip")

	return ign3File, nil
}

// Creates an Ign3 file whose contents are encoded according to
// https://datatracker.ietf.org/doc/html/rfc2397
func CreateEncodedIgn3File(path, content string, mode int) ign3types.File {
	encoded := dataurl.EncodeBytes([]byte(content))

	return CreateIgn3File(path, encoded, mode)
}

func CreateIgn3File(path, content string, mode int) ign3types.File {
	return ign3types.File{
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: &content,
			},
			Mode: &mode,
		},
		Node: ign3types.Node{
			Path: path,
			User: ign3types.NodeUser{
				Name: StrToPtr("root"),
			},
		},
	}
}

func MCDForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	return mcdForNode(cs, node)
}

// Returns the node's uptime expressed as the total number of seconds the
// system has been up.
// See: https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/6/html/deployment_guide/s2-proc-uptime
func GetNodeUptime(t *testing.T, cs *framework.ClientSet, node corev1.Node) float64 {
	output := ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/uptime")
	uptimeString := strings.Split(output, " ")[0]
	uptime, err := strconv.ParseFloat(uptimeString, 64)
	require.Nil(t, err)
	return uptime
}

// Asserts that a node has rebooted, given an initial uptime value.
//
// To Use:
//
// initialUptime := GetNodeUptime(t, cs, node)
// // do stuff that should cause a reboot
// AssertNodeReboot(t, cs, node, initialUptime)
//
// Returns the result of the assertion as is the convention for the testify/assert
func AssertNodeReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node, initialUptime float64) bool {
	current := GetNodeUptime(t, cs, node)
	return assert.Greater(t, initialUptime, current, "node %s did not reboot as expected; initial uptime: %d, current uptime: %d", node.Name, initialUptime, current)
}

// Asserts that a node has not rebooted, given an initial uptime value.
//
// To Use:
//
// initialUptime := GetNodeUptime(t, cs, node)
// // do stuff that should not cause a reboot
// AssertNodeNotReboot(t, cs, node, initialUptime)
//
// Returns the result of the assertion as is the convention for the testify/assert
func AssertNodeNotReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node, initialUptime float64) bool {
	current := GetNodeUptime(t, cs, node)
	return assert.Greater(t, current, initialUptime, "node %s rebooted unexpectedly; initial uptime: %d, current uptime: %d", node.Name, initialUptime, current)
}

// Overrides a global path variable by appending its original value onto a
// tempdir created by t.TempDir(). Returns a function that reverts it back. If
// you wanted to override the origParentDirPath, you would do something like
// this:
//
// cleanupFunc := OverrideGlobalPathVar(t, "origParentDirPath", &origParentDirPath)
// defer cleanupFunc()
//
// NOTE: testing.T will clean up any tempdirs it creates after each test, so
// there is no need to do something like defer os.RemoveAll().
func OverrideGlobalPathVar(t *testing.T, name string, val *string) func() {
	oldValue := *val

	newValue := filepath.Join(t.TempDir(), oldValue)

	*val = newValue

	t.Logf("original value of %s (%s) overridden with %s", name, oldValue, newValue)

	return func() {
		t.Logf("original value of %s restored to %s", name, oldValue)
		*val = oldValue
	}
}

type NodeOSRelease struct {
	// The contents of the /etc/os-release file
	EtcContent string
	// The contents of the /usr/lib/os-release file
	LibContent string
	// The parsed contents
	OS osrelease.OperatingSystem
}

// Retrieves the /etc/os-release and /usr/lib/os-release file contents on a
// given node and parses it through the OperatingSystem code.
func GetOSReleaseForNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) NodeOSRelease {
	etcOSReleaseContent := ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", osrelease.EtcOSReleasePath))
	libOSReleaseContent := ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", osrelease.LibOSReleasePath))

	os, err := osrelease.LoadOSRelease(etcOSReleaseContent, libOSReleaseContent)
	require.NoError(t, err)

	return NodeOSRelease{
		EtcContent: etcOSReleaseContent,
		LibContent: libOSReleaseContent,
		OS:         os,
	}
}

func mcdForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	// find the MCD pod that has spec.nodeNAME = node.Name and get its name:
	listOptions := metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}).String(),
	}
	listOptions.LabelSelector = labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String()

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	if len(mcdList.Items) != 1 {
		if len(mcdList.Items) == 0 {
			return nil, fmt.Errorf("failed to find MCD for node %s", node.Name)
		}
		return nil, fmt.Errorf("too many (%d) MCDs for node %s", len(mcdList.Items), node.Name)
	}
	return &mcdList.Items[0], nil
}

func DumpNodesAndPools(t *testing.T, nodes []*corev1.Node, pools []*mcfgv1.MachineConfigPool) {
	t.Helper()

	sb := &strings.Builder{}

	fmt.Fprintln(sb, "")
	fmt.Fprintln(sb, "===== Nodes =====")
	for _, node := range nodes {
		fmt.Fprintln(sb, dumpNode(node, false))
		fmt.Fprintln(sb, "=========")
	}
	fmt.Fprintln(sb, "")
	fmt.Fprintln(sb, "====== Pools ======")

	for _, pool := range pools {
		fmt.Fprintln(sb, dumpPool(pool, false))
		fmt.Fprintln(sb, "===================")
	}

	t.Log(sb.String())
}

func dumpNode(node *corev1.Node, silentNil bool) string {
	sb := &strings.Builder{}

	fmt.Fprintln(sb, "")

	if node != nil {
		if node.Name != "" {
			fmt.Fprintf(sb, "Node Name: %s\n", node.Name)
		}
		fmt.Fprintln(sb, "")
		fmt.Fprintf(sb, "Node Annotations: %s", spew.Sdump(node.Annotations))
		fmt.Fprintln(sb, "")
		fmt.Fprintf(sb, "Node Labels: %s", spew.Sdump(node.Labels))
		fmt.Fprintln(sb, "")

		conditions := "<empty>"
		if len(node.Status.Conditions) != 0 {
			conditions = spew.Sdump(node.Status.Conditions)
		}
		fmt.Fprintf(sb, "Node Conditions: %s\n", conditions)
	} else if !silentNil {
		fmt.Fprintln(sb, "Node was nil")
	}

	return sb.String()
}

func dumpPool(pool *mcfgv1.MachineConfigPool, silentNil bool) string {
	sb := &strings.Builder{}

	fmt.Fprintln(sb, "")

	if pool != nil {
		if pool.Name != "" {
			fmt.Fprintf(sb, "Pool Name: %q\n", pool.Name)
		}
		fmt.Fprintln(sb, "")
		fmt.Fprintf(sb, "Pool Annotations: %s", spew.Sdump(pool.Annotations))
		fmt.Fprintln(sb, "")
		fmt.Fprintf(sb, "Pool Labels: %s", spew.Sdump(pool.Labels))
		fmt.Fprintln(sb, "")
		fmt.Fprintf(sb, "Pool Config: %q\n", pool.Spec.Configuration.Name)
	} else if !silentNil {
		fmt.Fprintln(sb, "Pool was nil")
	}

	return sb.String()
}

// Deletes the node and machine objects from a cluster. This is intended to
// quickly remedy situations where a node degrades a MachineConfigPool and
// undoing it is very difficult. In IPI clusters in AWS, GCP, Azure, et. al.,
// the Machine API will provision a replacement node. For example, the
// e2e-layering tests cannot cleanly undo layering at this time, so we destroy
// the node / machine.
func DeleteNodeAndMachine(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	t.Helper()

	machineID := strings.ReplaceAll(node.Annotations["machine.openshift.io/machine"], machineAPINamespace+"/", "")

	ctx := context.Background()

	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())

	t.Logf("Deleting machine %s / node %s", machineID, node.Name)

	delErr := aggerrs.AggregateGoroutines(
		func() error {
			return machineClient.Machines(machineAPINamespace).Delete(ctx, machineID, metav1.DeleteOptions{})
		},
		func() error {
			return cs.CoreV1Interface.Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
		})

	require.NoError(t, delErr)
}

// Sets the MachineSet to the desired number of replicas. Note: Does not wait
// for new nodes to become available and does not wait for nodes to be scaled
// back down. Returns an idempotent function that will scale the nodes back to
// the original MachineSet replica value.
func ScaleMachineSet(t *testing.T, cs *framework.ClientSet, machinesetName string, desiredReplicas int32) func() {
	t.Helper()

	machineclient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())

	machineset, err := machineclient.MachineSets(machineAPINamespace).Get(context.TODO(), machinesetName, metav1.GetOptions{})
	require.NoError(t, err)

	originalReplicaCount := *machineset.Spec.Replicas
	t.Logf("MachineSet %s has original replica count of %d. Scaling to %d replicas.", machinesetName, originalReplicaCount, desiredReplicas)

	machineset.Spec.Replicas = &desiredReplicas

	_, err = machineclient.MachineSets(machineAPINamespace).Update(context.TODO(), machineset, metav1.UpdateOptions{})
	require.NoError(t, err)

	return MakeIdempotent(func() {
		machineset, err := machineclient.MachineSets(machineAPINamespace).Get(context.TODO(), machinesetName, metav1.GetOptions{})
		require.NoError(t, err)

		machineset.Spec.Replicas = &originalReplicaCount

		_, err = machineclient.MachineSets(machineAPINamespace).Update(context.TODO(), machineset, metav1.UpdateOptions{})
		require.NoError(t, err)

		t.Logf("Scaled MachineSet %s back to the original replica count of %d", machinesetName, originalReplicaCount)
	})
}

// Performs the same task as ScaleMachineSet, but waits for the new nodes to
// all be ready before returning. Returns a list of new nodes as well as an
// idempotent scaledown function that will scale the nodes back down and wait
// for all of the nodes to be ready.
func ScaleMachineSetAndWaitForNodesToBeReady(t *testing.T, cs *framework.ClientSet, machinesetName string, desiredReplicas int32) ([]*corev1.Node, func()) {
	t.Helper()

	ctx := context.Background()

	nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// Get a set of current node names before we scale up any additional nodes.
	originalNodes := nodeListToSet(nodes)

	start := time.Now()

	// Start the scale-up process
	scaledownFunc := ScaleMachineSet(t, cs, machinesetName, desiredReplicas)

	// New nodes means that the node is not part of the original set of nodes
	// that the cluster had before we began the scale-up process.
	newNodes := sets.New[string]()

	// Ready nodes means nodes that are "ready".
	readyNodes := sets.New[string]()

	// This is the final list of new and ready nodes that is returned to the caller.
	out := []*corev1.Node{}

	t.Log("Waiting for new node(s) to be ready")

	// Poll until the new nodes are present and ready.
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 20*time.Minute, false, func(ctx context.Context) (bool, error) {
		nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		// If we have the same nodes that we started with, it means new node(s)
		// haven't been created yet. We can return early from the polling function
		// because there's nothing to validate.
		currentNodes := nodeListToSet(nodes)
		if currentNodes.Equal(originalNodes) {
			return false, nil
		}

		for _, node := range nodes.Items {
			// Ignore the original cluster nodes before scaling up.
			if originalNodes.Has(node.Name) {
				continue
			}

			// We've already verified that this node is ready, skip it.
			if readyNodes.Has(node.Name) {
				continue
			}

			// If we don't have the node name in the originalNodes set, it means that
			// we've found a new node.
			if !originalNodes.Has(node.Name) && !newNodes.Has(node.Name) {
				t.Logf("Found new node %s after %v", node.Name, time.Since(start))
				newNodes.Insert(node.Name)
			}

			// If this is a new node and we haven't verified that it's ready yet, but
			// it is indeed ready, lets add it to our list of ready nodes.
			if newNodes.Has(node.Name) && !readyNodes.Has(node.Name) && isNodeReady(node) {
				t.Logf("Node %s is ready after %v", node.Name, time.Since(start))
				node := node
				readyNodes.Insert(node.Name)
				out = append(out, &node)
			}
		}

		// If the ready nodes are equal to the new nodes, we're done.
		return readyNodes.Equal(newNodes), nil
	})

	require.NoError(t, err)

	t.Logf("%d new nodes(s) %v ready after %s", readyNodes.Len(), sets.List(readyNodes), time.Since(start))

	scaledownAndWaitFunc := MakeIdempotent(func() {
		scaledownStart := time.Now()

		// Mark the new machines for deletion priority.
		for _, nodeName := range newNodes.UnsortedList() {
			require.NoError(t, setDeletionAnnotationOnMachineForNode(ctx, cs, nodeName))
		}

		// Call the scaledown function returned from the ScaleMachineSet function.
		scaledownFunc()

		waitTime := 5 * time.Minute

		t.Logf("Waiting up to %s for all nodes to be ready after scaledown of MachineSet %s", waitTime, machinesetName)

		// Wait for the nodes to be deleted and all nodes to be ready before we
		// continue further.
		err := wait.PollUntilContextTimeout(ctx, 2*time.Second, waitTime, true, func(ctx context.Context) (bool, error) {
			nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err
			}

			// Once we have the same number of nodes we started with and all of our
			// nodes are ready, we are done.
			return len(nodes.Items) == originalNodes.Len() && isAllNodesReady(nodes), nil
		})

		require.NoError(t, err)

		t.Logf("Scaledown complete after %s", time.Since(scaledownStart))
	})

	return out, scaledownAndWaitFunc
}

// Sets the delete-machine annotation on the underlying Machine object for a
// given node so that the Machine API will prioritize that machine for deletion
// when we scale back down.
// See: https://docs.openshift.com/container-platform/4.16/machine_management/deleting-machine.html
func setDeletionAnnotationOnMachineForNode(ctx context.Context, cs *framework.ClientSet, nodeName string) error {
	node, err := cs.CoreV1Interface.Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	machineID := strings.ReplaceAll(node.Annotations["machine.openshift.io/machine"], machineAPINamespace+"/", "")
	machineClient := machineClientv1beta1.NewForConfigOrDie(cs.GetRestConfig())

	m, err := machineClient.Machines(machineAPINamespace).Get(ctx, machineID, metav1.GetOptions{})
	if err != nil {
		return err
	}

	m.Annotations["machine.openshift.io/delete-machine"] = "true"
	_, err = machineClient.Machines(machineAPINamespace).Update(ctx, m, metav1.UpdateOptions{})
	return err
}

// Writes a file to a given node. Returns an idempotent cleanup function.
func WriteFileToNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, filename, contents string) func() {
	t.Helper()

	if !strings.HasPrefix(filename, "/rootfs") {
		filename = filepath.Join("/rootfs", filename)
	}

	bashCmd := fmt.Sprintf("printf '%s' > %s", contents, filename)
	t.Logf("Writing file %s to node %s", filename, node.Name)

	ExecCmdOnNode(t, cs, node, "/bin/bash", "-c", bashCmd)

	return MakeIdempotent(func() {
		t.Logf("Removing file %s from node %s", filename, node.Name)
		ExecCmdOnNode(t, cs, node, "rm", filename)
	})
}

// Polls the ControllerConfig and calls the provided condition function with
// the ControllerConfig to determine if the ControllerConfig has reached the
// expected state.
func AssertControllerConfigReachesExpectedState(t *testing.T, cs *framework.ClientSet, condFunc func(*mcfgv1.ControllerConfig) bool) {
	t.Helper()

	start := time.Now()
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, 5*time.Minute, true, func(pCtx context.Context) (bool, error) {
		cc, err := cs.ControllerConfigs().Get(pCtx, "machine-config-controller", metav1.GetOptions{})
		require.NoError(t, err)

		return condFunc(cc), nil
	})

	require.NoError(t, err, "ControllerConfig failed to reach expected state")
	t.Logf("ControllerConfig reached expected state in %v", time.Since(start))
}

// Polls all of the nodes and calls the provided condition function with each
// node to determine if the ControllerConfig has reached the expected state.
// Once the node has reached the expected state, it is skipped over for future
// iterations.
func AssertAllNodesReachExpectedState(t *testing.T, cs *framework.ClientSet, condFunc func(corev1.Node) bool) {
	t.Helper()

	nodeList, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	allNodes := nodeListToSet(nodeList)

	nodesWithDesiredConfig := sets.New[string]()

	start := time.Now()

	err = wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, 5*time.Minute, true, func(_ context.Context) (bool, error) {
		for _, node := range nodeList.Items {
			if nodesWithDesiredConfig.Has(node.Name) {
				continue
			}

			if condFunc(node) {
				nodesWithDesiredConfig.Insert(node.Name)
				t.Logf("Node %s reached desired state in %v", node.Name, time.Since(start))
			}
		}

		return allNodes.Equal(nodesWithDesiredConfig), nil
	})

	require.NoError(t, err, "%d nodes %v failed to reach desired state", allNodes.Len()-nodesWithDesiredConfig.Len(), allNodes.Difference(nodesWithDesiredConfig).UnsortedList())

	t.Logf("All nodes reached desired state in %v", time.Since(start))
}

// AssertMCDLogsContain asserts that the MCD pod's logs contains a target string value
func AssertMCDLogsContain(t *testing.T, cs *framework.ClientSet, mcdPod *corev1.Pod, node *corev1.Node, expectedContents string) {
	t.Helper()

	mcdLogs := getMCDLogs(t, cs, mcdPod, node)

	assert.Contains(t, mcdLogs, expectedContents, "expected to find '%s' in logs for %s/%s", expectedContents, mcdPod.Namespace, mcdPod.Name)
}

func getMCDLogs(t *testing.T, cs *framework.ClientSet, mcdPod *corev1.Pod, node *corev1.Node) string {
	t.Helper()

	ctx := context.Background()

	logs, err := cs.Pods(mcdPod.Namespace).GetLogs(mcdPod.Name, &corev1.PodLogOptions{
		Container: "machine-config-daemon",
	}).DoRaw(ctx)
	if err == nil {
		return string(logs)
	}

	// common err is that the mcd went down mid cmd. Re-try for good measure
	mcdPod, err = MCDForNode(cs, node)
	require.Nil(t, err)
	logs, err = cs.Pods(mcdPod.Namespace).GetLogs(mcdPod.Name, &corev1.PodLogOptions{
		Container: "machine-config-daemon",
	}).DoRaw(ctx)
	require.NoError(t, err)
	return string(logs)
}

func GetFunctionName(i interface{}) string {
	strs := strings.Split((runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()), ".")
	return strs[len(strs)-1]
}

func ShuffleSlice(slice interface{}) {
	v := reflect.ValueOf(slice)
	rand.Shuffle(v.Len(), func(i, j int) {
		vi := v.Index(i).Interface()
		v.Index(i).Set(v.Index(j))
		v.Index(j).Set(reflect.ValueOf(vi))
	})
}

func GetActionApplyConfiguration(action opv1.NodeDisruptionPolicySpecAction) *mcoac.NodeDisruptionPolicySpecActionApplyConfiguration {
	if action.Type == opv1.ReloadSpecAction {
		reloadApplyConfiguration := mcoac.ReloadService().WithServiceName(action.Reload.ServiceName)
		return mcoac.NodeDisruptionPolicySpecAction().WithType(action.Type).WithReload(reloadApplyConfiguration)
	}
	if action.Type == opv1.RestartSpecAction {
		restartApplyConfiguration := mcoac.RestartService().WithServiceName(action.Restart.ServiceName)
		return mcoac.NodeDisruptionPolicySpecAction().WithType(action.Type).WithRestart(restartApplyConfiguration)
	}
	return mcoac.NodeDisruptionPolicySpecAction().WithType(action.Type)
}

// Collects information from a node during a failed test that could be useful
// for debugging why the test failed.
// TODO: Wire this up to all of our tests so that we can get this info.
func CollectDebugInfoFromNode(t *testing.T, cs *framework.ClientSet, node *corev1.Node) {
	t.Helper()

	name := fmt.Sprintf("%s-%s", SanitizeTestName(t), node.Name)
	archiveName := fmt.Sprintf("%s.tar.gz", name)

	archive, err := NewArtifactArchive(t, archiveName)
	require.NoError(t, err)

	execCmdWithErr := func(cmd ...string) string {
		output, err := ExecCmdOnNodeWithError(cs, *node, cmd...)
		if err != nil {
			t.Logf("could not execute command: %s", strings.Join(cmd, " "))
		}
		return output
	}

	listDir := func(name string) string {
		return execCmdWithErr("ls", "-la", filepath.Join("/rootfs", name))
	}

	catFileWithErr := func(name string) (string, error) {
		return ExecCmdOnNodeWithError(cs, *node, "cat", filepath.Join("/rootfs", name))
	}

	runCmd := func(cmd []string) string {
		cmd = append([]string{"chroot", "/rootfs"}, cmd...)
		return execCmdWithErr(cmd...)
	}

	mcdPod, err := MCDForNode(cs, node)
	require.NoError(t, err)

	writeFileToArchive := func(path, contents string) {
		archiveFilename := filepath.Join(archive.StagingDir(), path)
		require.NoError(t, err, os.MkdirAll(filepath.Dir(archiveFilename), 0o755))
		require.NoError(t, os.WriteFile(archiveFilename, []byte(contents), 0o755))
		t.Logf("Wrote %s", archiveFilename)
	}

	writeNodeFileToArchive := func(path, contents string) {
		writeFileToArchive(filepath.Join(node.Name, path), contents)
	}

	itemsToWrite := map[string]string{
		"ssh-ls.log":                       listDir("/home/core/.ssh"),
		"etc-mco-ls.log":                   listDir("/etc/mco"),
		"etc-ls.log":                       listDir("/etc"),
		"etc-machine-config-daemon-ls.log": listDir("/etc/machine-config-daemon"),
		"journalctl-listboots.log":         runCmd([]string{"journalctl", "--list-boots"}),
		"rpm-ostree-status.log":            runCmd([]string{"rpm-ostree", "status"}),
		"mcd.log":                          getMCDLogs(t, cs, mcdPod, node),
	}

	for filename, contents := range itemsToWrite {
		writeFileToArchive(filepath.Join("state", filename), contents)
	}

	nodeFilesToWrite := []string{
		"/etc/machine-config-daemon/currentconfig",
		"/etc/os-release",
		"/usr/lib/osrelease",
		constants.RHCOS8SSHKeyPath,
		constants.RHCOS9SSHKeyPath,
		"/etc/machine-config-daemon/node-annotation.json.bak",
		"/etc/ignition-machine-config-encapsulated.json.bak",
	}

	for _, filename := range nodeFilesToWrite {
		contents, err := catFileWithErr(filename)
		if err != nil {
			t.Logf("could not get file %s from node, skipping", filename)
			continue
		}
		writeNodeFileToArchive(filename, contents)
	}

	ctx := context.Background()

	updatedNode, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	mc, err := cs.MachineConfigs().Get(ctx, node.Annotations["machineconfiguration.openshift.io/desiredConfig"], metav1.GetOptions{})
	require.NoError(t, err)

	mcp, err := cs.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	objectsToWrite := map[string]interface{}{
		"node.yaml":          updatedNode,
		"machineconfig.yaml": mc,
		"mcp.yaml":           mcp,
	}

	for filename, object := range objectsToWrite {
		out, err := yaml.Marshal(object)
		require.NoError(t, err)
		writeFileToArchive(filepath.Join("objects", filename), string(out))
	}

	// Consider retrieving the MachineConfig contents from the node filesystem
	// once the circular import between this package (test/helpers) and
	// ctrlcommon (pkg/controller/common) is resolved.

	require.NoError(t, archive.WriteArchive())
}

// Sets labels on any object that is created for this test. Includes the name
// of the currently running test. This is intended to aid debugging failed
// tests.
func SetMetadataOnObject(t *testing.T, obj metav1.Object) {
	setAnnotationsOnObject(t, obj)
	setLabelsOnObject(obj)
}

func setAnnotationsOnObject(t *testing.T, obj metav1.Object) {
	annos := obj.GetAnnotations()
	if annos == nil {
		annos = map[string]string{}
	}

	annos[UsedByE2ETestAnnoKey] = t.Name()

	obj.SetAnnotations(annos)
}

func setLabelsOnObject(obj metav1.Object) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	labels[UsedByE2ETestLabelKey] = ""

	obj.SetLabels(labels)
}

// Converts a given NodeList to a Set.
// See: https://github.com/kubernetes/apimachinery/tree/master/pkg/util/sets
func nodeListToSet(nodeList *corev1.NodeList) sets.Set[string] {
	nodes := sets.New[string]()

	for _, node := range nodeList.Items {
		nodes.Insert(node.Name)
	}

	return nodes
}

func SetContainerfileContentsOnMachineOSConfig(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1.MachineOSConfig, contents string) *mcfgv1.MachineOSConfig {
	t.Helper()

	apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	require.NoError(t, err)

	apiMosc.Spec.Containerfile = []mcfgv1.MachineOSContainerfile{
		{
			ContainerfileArch: mcfgv1.NoArch,
			Content:           contents,
		},
	}

	apiMosc, err = mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	return apiMosc
}

func SetRebuildAnnotationOnMachineOSConfig(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1.MachineOSConfig) {
	t.Helper()

	apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	require.NoError(t, err)

	apiMosc.Annotations[buildConstants.RebuildMachineOSConfigAnnotationKey] = ""

	_, err = mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)
}
