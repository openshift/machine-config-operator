package e2e_2of2_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/pprof"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestPprofConfigMap validates that pprof can be enabled/disabled via ConfigMap
// for all MCO components
func TestPprofConfigMap(t *testing.T) {
	cs := framework.NewClientSet("")

	// Use test context (respects go test -timeout flag)
	ctx := t.Context()

	// Ensure ConfigMap doesn't exist at the start
	_ = cs.CoreV1Interface.ConfigMaps(common.MCONamespace).Delete(
		ctx,
		pprof.EnablePprofConfigMapName,
		metav1.DeleteOptions{},
	)

	// Give reconciliation time to process deletion if it existed
	time.Sleep(5 * time.Second)

	// Test components with their expected ports
	components := []struct {
		name           string
		deploymentKind string // "Deployment" or "DaemonSet"
		port           int
	}{
		{name: "machine-config-controller", deploymentKind: "Deployment", port: 6060},
		{name: "machine-config-daemon", deploymentKind: "DaemonSet", port: 6061},
		{name: "machine-config-operator", deploymentKind: "Deployment", port: 6062},
		{name: "machine-config-server", deploymentKind: "DaemonSet", port: 6063},
	}

	// Step 1: Create enable-pprof ConfigMap
	t.Log("Creating enable-pprof ConfigMap...")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pprof.EnablePprofConfigMapName,
			Namespace: common.MCONamespace,
		},
	}
	_, err := cs.CoreV1Interface.ConfigMaps(common.MCONamespace).Create(
		ctx,
		cm,
		metav1.CreateOptions{},
	)
	require.NoError(t, err, "Failed to create enable-pprof ConfigMap")

	// Cleanup: delete ConfigMap at the end (deferred, runs even if test fails)
	t.Cleanup(func() {
		t.Log("Cleaning up: Deleting enable-pprof ConfigMap...")
		// Use background context since test context is cancelled at this point
		err := cs.CoreV1Interface.ConfigMaps(common.MCONamespace).Delete(
			context.Background(),
			pprof.EnablePprofConfigMapName,
			metav1.DeleteOptions{},
		)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("Warning: Failed to cleanup ConfigMap: %v", err)
		}
	})

	// Step 2: Wait for rollout and verify pprof is enabled
	// Each component waits for its manifest to update, pods to rollout, and verifies pprof access
	// Note: We don't use t.Parallel() here because we need all components to complete
	// before deleting the ConfigMap in the next step
	t.Log("Waiting for components to enable pprof and verifying access...")
	for _, component := range components {
		component := component // Capture for goroutine
		t.Run(fmt.Sprintf("EnableAndVerify_%s", component.name), func(t *testing.T) {
			// Special handling for MCO - it uses dynamic startup, not manifest args
			if component.name == "machine-config-operator" {
				t.Logf("Waiting for %s to enable pprof dynamically...", component.name)
				// MCO doesn't update its manifest, just wait for pods to be ready
				err := waitForPodsReady(ctx, t, cs, component.name, false)
				require.NoError(t, err, "Pods for %s not ready", component.name)

				// Give MCO time to reconcile and start pprof dynamically
				time.Sleep(15 * time.Second)

				// Verify pprof is accessible
				verifyPprofEnabled(ctx, t, cs, component.name, component.port)
				return
			}

			// For other components, wait for manifest to update
			if component.deploymentKind == "Deployment" {
				t.Logf("Waiting for deployment %s to rollout with pprof args...", component.name)
				err := waitForDeploymentWithPprofArgs(ctx, cs, component.name)
				require.NoError(t, err, "Deployment %s did not get pprof args", component.name)

				// Wait for pods to be ready with pprof args
				err = waitForPodsReady(ctx, t, cs, component.name, true)
				require.NoError(t, err, "Pods for %s not ready with pprof args", component.name)
			} else {
				// DaemonSet
				t.Logf("Waiting for daemonset %s to rollout with pprof args...", component.name)
				err := waitForDaemonSetWithPprofArgs(ctx, cs, component.name)
				require.NoError(t, err, "DaemonSet %s did not get pprof args", component.name)

				// Wait for pods to be ready with pprof args
				err = waitForPodsReady(ctx, t, cs, component.name, true)
				require.NoError(t, err, "Pods for %s not ready with pprof args", component.name)
			}

			// Verify pprof is accessible
			verifyPprofEnabled(ctx, t, cs, component.name, component.port)
		})
	}

	// Step 3: Delete ConfigMap to disable pprof
	t.Log("Deleting enable-pprof ConfigMap to test disablement...")
	err = cs.CoreV1Interface.ConfigMaps(common.MCONamespace).Delete(
		ctx,
		pprof.EnablePprofConfigMapName,
		metav1.DeleteOptions{},
	)
	require.NoError(t, err, "Failed to delete enable-pprof ConfigMap")

	// Step 4: Verify pprof endpoints are disabled (parallelized)
	for _, component := range components {
		component := component // Capture for goroutine
		t.Run(fmt.Sprintf("VerifyDisabled_%s", component.name), func(t *testing.T) {
			t.Parallel()
			t.Logf("Waiting for pprof to be disabled for %s...", component.name)
			// Poll until pprof is disabled or timeout
			err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(_ context.Context) (bool, error) {
				disabled := verifyPprofDisabled(ctx, t, cs, component.name, component.port)
				if disabled {
					t.Logf("✓ pprof disabled for %s", component.name)
					return true, nil
				}
				t.Logf("pprof still accessible for %s, retrying...", component.name)
				return false, nil
			})
			require.NoError(t, err, "Timeout waiting for pprof to be disabled for %s", component.name)
		})
	}
}

// hasPprofArgs checks if any container in the pod spec has pprof flags
func hasPprofArgs(podSpec corev1.PodSpec) bool {
	for _, container := range podSpec.Containers {
		hasPprofEnabled := false
		hasPprofPort := false

		for _, arg := range container.Args {
			if arg == "--enable-pprof" {
				hasPprofEnabled = true
			}

			if strings.HasPrefix(arg, "--pprof-port=") {
				hasPprofPort = true
			}
		}

		// If this container has both pprof flags, return true
		if hasPprofEnabled && hasPprofPort {
			return true
		}
	}

	// No container had both pprof flags
	return false
}

// waitForDeploymentWithPprofArgs waits for a deployment to have pprof args in its pod template
func waitForDeploymentWithPprofArgs(ctx context.Context, cs *framework.ClientSet, name string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(_ context.Context) (bool, error) {
		deployment, err := cs.AppsV1Interface.Deployments(common.MCONamespace).Get(
			ctx,
			name,
			metav1.GetOptions{},
		)
		if err != nil {
			// For all errors (transient API errors), retry
			return false, nil
		}

		// Check if the pod template has pprof args
		return hasPprofArgs(deployment.Spec.Template.Spec), nil
	})
}

// waitForDaemonSetWithPprofArgs waits for a daemonset to have pprof args in its pod template
func waitForDaemonSetWithPprofArgs(ctx context.Context, cs *framework.ClientSet, name string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(_ context.Context) (bool, error) {
		daemonset, err := cs.AppsV1Interface.DaemonSets(common.MCONamespace).Get(
			ctx,
			name,
			metav1.GetOptions{},
		)
		if err != nil {
			// For all errors (transient API errors), retry
			return false, nil
		}

		// Check if the pod template has pprof args
		return hasPprofArgs(daemonset.Spec.Template.Spec), nil
	})
}

// waitForPodsReady waits for pods of a component to be ready with the expected pprof configuration
// expectPprofArgs: true = wait for pods WITH pprof args, false = wait for pods WITHOUT pprof args
func waitForPodsReady(ctx context.Context, t *testing.T, cs *framework.ClientSet, componentName string, expectPprofArgs bool) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(_ context.Context) (bool, error) {
		pod := getFirstAvailablePod(ctx, t, cs, componentName, expectPprofArgs)
		return pod != nil, nil
	})
}

// getFirstAvailablePod returns the first ready pod for a component with the expected pprof configuration
// expectPprofArgs: true = find pod WITH pprof args, false = find pod WITHOUT pprof args
// Returns nil if no matching ready pods are found (all errors are treated as retryable)
func getFirstAvailablePod(ctx context.Context, t *testing.T, cs *framework.ClientSet, componentName string, expectPprofArgs bool) *corev1.Pod {
	pods, err := cs.CoreV1Interface.Pods(common.MCONamespace).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: fmt.Sprintf("k8s-app=%s", componentName),
		},
	)
	if err != nil {
		// Log API errors but treat as retryable
		t.Logf("Warning: Failed to list pods for %s: %v", componentName, err)
		return nil
	}

	if len(pods.Items) == 0 {
		// No pods found - this is retryable (pods may be creating)
		t.Logf("Warning: No pods found for component %s", componentName)
		return nil
	}

	// Track statistics for debugging
	var readyCount, wrongConfigCount int

	// Find the first ready pod with the expected pprof configuration
	for i := range pods.Items {
		pod := &pods.Items[i]

		// Check if pod is ready
		isReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				isReady = true
				break
			}
		}

		if !isReady {
			continue
		}

		readyCount++

		// Check if pod has the expected pprof configuration
		podHasPprofArgs := hasPprofArgs(pod.Spec)
		if podHasPprofArgs == expectPprofArgs {
			return pod
		}

		wrongConfigCount++
	}

	// No matching pod found - log helpful debug info (retryable)
	if readyCount == 0 {
		t.Logf("Warning: Found %d pod(s) for %s but none are ready", len(pods.Items), componentName)
	} else if wrongConfigCount > 0 {
		expectedStr := "WITHOUT"
		if expectPprofArgs {
			expectedStr = "WITH"
		}
		t.Logf("Warning: Found %d ready pod(s) for %s but none have expected pprof config (%s pprof args)",
			readyCount, componentName, expectedStr)
	}

	return nil
}

// verifyPprofEnabled verifies that pprof endpoint is accessible for a component
func verifyPprofEnabled(ctx context.Context, t *testing.T, cs *framework.ClientSet, componentName string, port int) {
	// For MCO, pprof is enabled dynamically, not via pod args, so expectPprofArgs=false
	// For other components, pprof is in the pod args, so expectPprofArgs=true
	expectPprofArgs := componentName != "machine-config-operator"
	testPod := getFirstAvailablePod(ctx, t, cs, componentName, expectPprofArgs)
	require.NotNil(t, testPod, "No ready pods found for %s with expected pprof configuration", componentName)

	t.Logf("Testing pprof endpoint for %s (pod: %s, port: %d)", componentName, testPod.Name, port)

	// Use curl from within the pod to access localhost pprof endpoint
	url := fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port)
	output, err := execCmdOnPod(cs, testPod.Namespace, testPod.Name, componentName, "curl", "-s", "-m", "5", url)
	require.NoError(t, err, "Failed to exec curl on pod %s", testPod.Name)

	// Verify the output contains pprof index page markers
	assert.Contains(t, output, "/debug/pprof", "pprof endpoint should return index page for %s", componentName)
	t.Logf("✓ pprof endpoint is accessible for %s", componentName)
}

// verifyPprofDisabled checks if pprof endpoint is NOT accessible for a component
// Returns true if disabled/inaccessible, false if still accessible or can't verify
func verifyPprofDisabled(ctx context.Context, t *testing.T, cs *framework.ClientSet, componentName string, port int) bool {
	// After disabling pprof, we expect pods WITHOUT pprof args (old pods or newly rolled out without pprof)
	// For MCO, it never has pprof in args anyway, so expectPprofArgs=false
	// For other components, after ConfigMap deletion and rollout, pods should not have pprof args
	testPod := getFirstAvailablePod(ctx, t, cs, componentName, false)
	if testPod == nil {
		// No ready pods found without pprof args - still rolling out, return false to retry
		return false
	}

	// Use curl from within the pod to try accessing localhost pprof endpoint
	url := fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port)
	output, err := execCmdOnPod(cs, testPod.Namespace, testPod.Name, componentName, "curl", "-s", "-m", "5", url)

	// We expect this to fail (connection refused) since pprof should be stopped
	if err == nil && strings.Contains(output, "/debug/pprof") {
		// Still accessible - return false
		return false
	}

	// Not accessible - return true (disabled)
	return true
}

// execCmdOnPod executes a command in a specific pod/container using oc rsh
func execCmdOnPod(cs *framework.ClientSet, namespace, podName, containerName string, subArgs ...string) (string, error) {
	args := []string{
		"rsh",
		"-n", namespace,
		"-c", containerName,
		podName,
	}
	args = append(args, subArgs...)

	cmd, err := helpers.NewOcCommand(cs, args...)
	if err != nil {
		return "", err
	}

	output, err := cmd.CombinedOutput()
	return string(output), err
}
