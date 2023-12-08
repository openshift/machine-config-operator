package e2e_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

func createConfigMapForTest(t *testing.T, cs *framework.ClientSet) func() {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	secretName := ""
	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			secretName = secret.Name
			break
		}
	}

	t.Logf("Using secret name %q for test", secretName)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      build.OnClusterBuildConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			build.BaseImagePullSecretNameConfigKey:  secretName,
			build.FinalImagePushSecretNameConfigKey: secretName,
			build.FinalImagePullspecConfigKey:       "registry.host.com/org/repo:tag",
		},
	}

	_, err = cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("ConfigMap %q created for test", cm.Name)

	cleanup := helpers.MakeIdempotent(func() {
		err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
		t.Logf("ConfigMap %q deleted", cm.Name)
	})

	t.Cleanup(cleanup)
	return cleanup
}

func TestMachineOSBuilder(t *testing.T) {
	//add an assertion to see if deployment object is present or absent
	//correlate that with pod detection

	cs := framework.NewClientSet("")
	mcpName := "test-mcp"
	namespace := "openshift-machine-config-operator"
	mobPodNamePrefix := "machine-os-builder"

	// get the feature gates because we're gating this for now
	featureGates, err := cs.ConfigV1Interface.FeatureGates().Get(context.TODO(), "cluster", metav1.GetOptions{})
	require.NoError(t, err, "Failed to retrieve feature gates")

	// TODO(jkyros): this should be a helper or we should use whatever the "best practice" way is
	// for retrieving the gates during a test, but this works for now
	var featureGateEnabled bool
	for _, featureGateDetails := range featureGates.Status.FeatureGates {
		for _, enabled := range featureGateDetails.Enabled {
			if enabled.Name == "OnClusterBuild" {
				featureGateEnabled = true

			}
		}
	}

	t.Logf("Feature gate OnClusterBuiild enabled: %t", featureGateEnabled)

	t.Cleanup(createConfigMapForTest(t, cs))

	cleanup := helpers.MakeIdempotent(helpers.CreateMCP(t, cs, mcpName))
	t.Cleanup(cleanup)
	time.Sleep(10 * time.Second) // Wait a bit to ensure MCP is fully created

	// added retry because another process was modifying the MCP concurrently
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest MCP
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Set the label
		mcp.ObjectMeta.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

		// Try to update the MCP
		_, err = cs.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		return err
	})
	require.Nil(t, retryErr)

	// assertion to see if the deployment object is present after setting the label
	ctx := context.TODO()
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		exists, err := helpers.CheckDeploymentExists(cs, "machine-os-builder", namespace)
		return exists, err
	})

	if featureGateEnabled {
		require.NoError(t, err, "Failed to check the existence of the Machine OS Builder deployment")

		// wait for Machine OS Builder pod to start
		err = helpers.WaitForPodStart(cs, mobPodNamePrefix, namespace)
		require.NoError(t, err, "Failed to start the Machine OS Builder pod")
		t.Logf("machine-os-builder deployment exists")

		// delete the MachineConfigPool
		cleanup()
		time.Sleep(20 * time.Second)

		// assertion to see if the deployment object is absent after deleting the MCP
		err = wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
			exists, err := helpers.CheckDeploymentExists(cs, "machine-os-builder", namespace)
			return !exists, err
		})
		require.NoError(t, err, "Failed to check the absence of the Machine OS Builder deployment")

		// wait for Machine OS Builder pod to stop
		err = helpers.WaitForPodStop(cs, mobPodNamePrefix, namespace)
		require.NoError(t, err, "Failed to stop the Machine OS Builder pod")
		t.Logf("machine-os-builder deployment no longer exists")

		_, err = cs.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-os-builder", metav1.GetOptions{})
		assert.True(t, apierrs.IsNotFound(err), "machine-os-builder deployment still present")
	} else {
		require.Error(t, err, "Machine OS Builder deployment exists and it should not, because the feature gate is disabled")
	}
}
