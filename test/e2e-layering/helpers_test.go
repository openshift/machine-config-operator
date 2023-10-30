package e2e_layering

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// Identifies a secret in the MCO namespace that has permissions to push to the ImageStream used for the test.
func getBuilderPushSecretName(cs *framework.ClientSet) (string, error) {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			return secret.Name, nil
		}
	}

	return "", fmt.Errorf("could not find matching secret name in namespace %s", ctrlcommon.MCONamespace)
}

// Gets the ImageStream pullspec for the ImageStream used for the test.
func getImagestreamPullspec(cs *framework.ClientSet, name string) (string, error) {
	is, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s:latest", is.Status.DockerImageRepository), nil
}

// Creates an OpenShift ImageStream in the MCO namespace for the test and
// registers a cleanup function.
func createImagestream(t *testing.T, cs *framework.ClientSet, name string) func() {
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MCONamespace,
		},
	}

	_, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Create(context.TODO(), is, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ImageStream %q", name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Delete(context.TODO(), name, metav1.DeleteOptions{}))
		t.Logf("Deleted ImageStream %q", name)
	})
}

// Creates the on-cluster-build-custom-dockerfile ConfigMap and registers a cleanup function.
func createCustomDockerfileConfigMap(t *testing.T, cs *framework.ClientSet, customDockerfiles map[string]string) func() {
	return createConfigMap(t, cs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "on-cluster-build-custom-dockerfile",
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: customDockerfiles,
	})
}

// Creates a given ConfigMap and registers a cleanup function to delete it.
func createConfigMap(t *testing.T, cs *framework.ClientSet, cm *corev1.ConfigMap) func() {
	_, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ConfigMap %q", cm.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{}))
		klog.Infof("Deleted ConfigMap %q", cm.Name)
	})
}

// Creates a given Secret and registers a cleanup function to delete it.
func createSecret(t *testing.T, cs *framework.ClientSet, secret *corev1.Secret) func() {
	_, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created secret %q", secret.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted secret %q", secret.Name)
	})
}

// Copies the global pull secret from openshift-config/pull-secret into the MCO
// namespace so that it can be used by the build processes.
func copyGlobalPullSecret(t *testing.T, cs *framework.ClientSet) func() {
	globalPullSecret, err := cs.CoreV1Interface.Secrets("openshift-config").Get(context.TODO(), "pull-secret", metav1.GetOptions{})
	require.NoError(t, err)

	secretCopy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globalPullSecretCloneName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: globalPullSecret.Data,
		Type: globalPullSecret.Type,
	}

	cleanup := createSecret(t, cs, secretCopy)
	t.Logf("Cloned global pull secret %q into namespace %q as %q", "pull-secret", ctrlcommon.MCONamespace, secretCopy.Name)

	return makeIdempotentAndRegister(t, func() {
		cleanup()
		t.Logf("Deleted global pull secret copy %q", secretCopy.Name)
	})
}

// Waits for the target MachineConfigPool to reach a state defined in a supplied function.
func waitForPoolToReachState(t *testing.T, cs *framework.ClientSet, poolName string, condFunc func(*mcfgv1.MachineConfigPool) bool) {
	ctx, cancel := context.WithTimeout(context.TODO(), 15*time.Second)
	defer cancel()

	condition := func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		lps := ctrlcommon.NewLayeredPoolState(mcp)
		hasOSImage := lps.HasOSImage()
		isBuildSuccess := lps.IsBuildSuccess()
		isBuildPending := lps.IsBuildPending()
		IsBuilding := lps.IsBuilding()
		isLayered := lps.IsLayered()

		t.Logf("Checking state for MCP %s: HasOSImage- %v, IsBuildSuccess- %v, IsBuildPending- %v, IsBuilding- %v, isLayered- %v", poolName, hasOSImage, isBuildSuccess, isBuildPending, IsBuilding, isLayered)
		t.Logf("MCP %s Status: %+v", poolName, mcp.Status)

		return condFunc(mcp), nil
	}

	err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 10*time.Minute, true, condition)
	require.NoError(t, err, "MachineConfigPool %q did not reach desired state", poolName)
}

// Registers a cleanup function, making it idempotent, and wiring up the
// skip-cleanup flag to it which will cause cleanup to be skipped, if set.
func makeIdempotentAndRegister(t *testing.T, cleanupFunc func()) func() {
	out := helpers.MakeIdempotent(func() {
		if !skipCleanup {
			cleanupFunc()
		}
	})
	t.Cleanup(out)
	return out
}

// Check if a specific deployment exists in a namespace.
func CheckDeploymentExists(t *testing.T, cs *framework.ClientSet, deploymentName, namespace string) bool {
	_, err := cs.AppsV1Interface.Deployments(namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false
		}
		t.Fatalf("Error fetching deployment: %v", err)
	}
	return true
}
