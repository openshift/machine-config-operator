package e2e_techpreview_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

func createMachineOSConfig(t *testing.T, cs *framework.ClientSet, mosc *mcfgv1alpha1.MachineOSConfig) func() {
	_, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Create(context.TODO(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created MachineOSConfig %q", mosc.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Delete(context.TODO(), mosc.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted MachineOSConfig %q", mosc.Name)
	})
}

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

func waitForMachineOSBuildToReachState(t *testing.T, cs *framework.ClientSet, poolName string, condFunc func(*mcfgv1alpha1.MachineOSBuild, error) (bool, error)) {
	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mosbName := fmt.Sprintf("%s-%s-builder", poolName, mcp.Spec.Configuration.Name)

	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		mosb, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Get(context.TODO(), mosbName, metav1.GetOptions{})

		return condFunc(mosb, err)
	})

	require.NoError(t, err, "MachineOSBuild %q did not reach desired state", mosbName)
}

// Waits for the target MachineConfigPool to reach a state defined in a supplied function.
func waitForPoolToReachState(t *testing.T, cs *framework.ClientSet, poolName string, condFunc func(*mcfgv1.MachineConfigPool) bool) {
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return condFunc(mcp), nil
	})

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

// TOOD: Refactor into smaller functions.
func cleanupEphemeralBuildObjects(t *testing.T, cs *framework.ClientSet) {
	// TODO: Instantiate this by using the label selector library.
	labelSelector := "machineconfiguration.openshift.io/desiredConfig,machineconfiguration.openshift.io/buildPod,machineconfiguration.openshift.io/targetMachineConfigPool"

	cmList, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	require.NoError(t, err)

	podList, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	require.NoError(t, err)

	mosbList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	moscList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	if len(cmList.Items) == 0 {
		t.Logf("No ephemeral ConfigMaps to clean up")
	}

	if len(podList.Items) == 0 {
		t.Logf("No build pods to clean up")
	}

	if len(mosbList.Items) == 0 {
		t.Logf("No MachineOSBuilds to clean up")
	}

	if len(moscList.Items) == 0 {
		t.Logf("No MachineOSConfigs to clean up")
	}

	for _, item := range cmList.Items {
		t.Logf("Cleaning up ephemeral ConfigMap %q", item.Name)
		require.NoError(t, cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}

	for _, item := range podList.Items {
		t.Logf("Cleaning up build pod %q", item.Name)
		require.NoError(t, cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}

	for _, item := range moscList.Items {
		t.Logf("Cleaning up MachineOSConfig %q", item.Name)
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}

	for _, item := range mosbList.Items {
		t.Logf("Cleaning up MachineOSBuild %q", item.Name)
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}
}
