package envtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	goruntime "runtime"

	configv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
)

const (
	OpenshiftConfigNamespace string = "openshift-config"

	// TODO: Figure out how to obtain this value programmatically so we don't
	// have to remember to increment it.
	k8sVersion string = "1.22.1"
)

// This is needed because both setup-envtest and the kubebuilder tools assume
// that HOME is set to a writable value. While running locally, this is
// generally a given, it is not in OpenShift CI. OpenShift CI usually defaults
// to "/" as the HOME value, which is not writable.
//
// TODO: Pre-fetch the kubebuilder binaries as part of the build-root process
// so we only have to fetch the kubebuilder tools once.
func overrideHomeDir(t *testing.T) (string, bool) {
	homeDir := os.Getenv("HOME")
	if homeDir != "" && homeDir != "/" {
		t.Logf("HOME env var set to %s, will use as-is", homeDir)
		return "", false
	}

	// For the purposes of this library, we will use the repo root
	// (/go/src/github.com/openshift/machine-config-operator). This is so that we
	// have a predictable HOME value which enables setup-envtest to reuse a
	// kubebuilder tool package across multiple test suites (assuming they run in
	// the same pod).
	overrideHomeDir, err := os.Getwd()
	require.NoError(t, err)

	if homeDir == "/" {
		t.Log("HOME env var set to '/', overriding with", overrideHomeDir)
		return overrideHomeDir, true
	}

	t.Log("HOME env var not set, overriding with", overrideHomeDir)
	return overrideHomeDir, true
}

// Instead of using a couple of ad-hoc shell scripts, envtest helpfully
// includes a setup utility (setup-envtest) that will retrieve the appropriate
// version of the kubebuilder toolchain for a given GOOS / GOARCH and K8s
// version. setup-envtest can also allow the toolchain to be prefetched and
// will cache it. This way, if multiple envtest targets are running in the same
// CI test pod, it will only fetch kubebuilder for the first suite.
func setupEnvTest(t *testing.T) (string, error) {
	setupEnvTestBinPath, err := exec.LookPath("setup-envtest")
	if err != nil {
		return "", fmt.Errorf("setup-envtest not installed, see installation instructions: https://github.com/kubernetes-sigs/controller-runtime/tree/master/tools/setup-envtest")
	}

	homeDir, overrideHomeDir := overrideHomeDir(t)

	if overrideHomeDir {
		os.Setenv("HOME", homeDir)
	}

	cmd := exec.Command(setupEnvTestBinPath, "use", k8sVersion, "-p", "path")
	t.Log("Setting up EnvTest: $", cmd)

	// We want to consume the path of where setup-envtest installed the
	// kubebuilder toolchain. So we capture stdout from setup-envtest (as well as
	// write it to os.Stdout for debugging purposes).
	pathBuffer := bytes.NewBuffer([]byte{})
	cmd.Stdout = io.MultiWriter(pathBuffer, os.Stdout)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("could not fetch envtest archive: %w", err)
	}

	return pathBuffer.String(), nil
}

// This gets the repo root by using runtime.Caller(0) to figure out what file
// this is. Failing that, it will use relative paths from this package to the
// repo root. This really isn't the best way to do this.
// TODO: Figure out a better way to load and reference the manifest files.
func getRepoRoot(t *testing.T) string {
	repoRootDirname := "machine-config-operator"
	bestGuess := "../../.."

	_, thisFilename, _, ok := goruntime.Caller(0)
	if !ok {
		t.Logf("could not programmatically determine repo root, using best guess %s", bestGuess)
		return bestGuess
	}

	if !strings.Contains(thisFilename, repoRootDirname) {
		t.Logf("could not find repo root (%s) in our path, using best guess %s", repoRootDirname, bestGuess)
	}

	// Walk up the path until we're at the repo root level
	for !strings.HasSuffix(thisFilename, repoRootDirname) {
		thisFilename = filepath.Dir(thisFilename)
	}

	return thisFilename
}

func NewTestEnv(t *testing.T) *envtest.Environment {
	var toolsPath string
	helpers.TimeIt(t, "setup-envtest complete", func() {
		path, err := setupEnvTest(t)
		require.NoError(t, err)
		toolsPath = path
	})

	repoRoot := getRepoRoot(t)
	t.Logf("using %s as repo root", repoRoot)

	return &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				filepath.Join(repoRoot, "install"),
				filepath.Join(repoRoot, "manifests", "controllerconfig.crd.yaml"),
				filepath.Join(repoRoot, "vendor", "github.com", "openshift", "api", "config", "v1"),
				filepath.Join(repoRoot, "vendor", "github.com", "openshift", "api", "operator", "v1alpha1"),
			},
			CleanUpAfterUse: true,
		},
		BinaryAssetsDirectory: toolsPath,
	}
}

// EnvTest creates a few ConfigMaps when it starts up. We want to make sure
// that these do not affect our cleared count, so we account for them here.
func checkCleanConfigMaps(configmapList *corev1.ConfigMapList, namespaceName string) error {
	if len(configmapList.Items) == 0 {
		return nil
	}

	// This configmap is placed there by the kube-apiserver. We should not error whenever we encounter it.
	if len(configmapList.Items) == 1 && namespaceName == "kube-system" && configmapList.Items[0].Name == "extension-apiserver-authentication" {
		return nil
	}

	return fmt.Errorf("expected no ConfigMaps for namespace %s, found %d", namespaceName, len(configmapList.Items))
}

// checkCleanEnvironment checks that all of the resource types that are to be used in this test currently have no items.
// This ensures that no atifacts from previous test runs are interfering with the current test.
func CheckCleanEnvironment(t *testing.T, clientSet *framework.ClientSet) {
	t.Helper()

	ctx := context.Background()

	requireNoObjects := func(objs runtime.Object, err error) {
		require.NoError(t, err)
		require.Equal(t, meta.LenList(objs), 0)
	}

	// ########################################
	// BEGIN: machineconfiguration.openshift.io
	// ########################################
	requireNoObjects(clientSet.ContainerRuntimeConfigs().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.ControllerConfigs().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.KubeletConfigs().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.MachineConfigPools().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.MachineConfigs().List(ctx, metav1.ListOptions{}))
	// ######################################
	// END: machineconfiguration.openshift.io
	// ######################################

	// #############
	// BEGIN: "core"
	// #############
	namespaceList, err := clientSet.Namespaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// Iterate through each namespace for namespace-scoped objects so we can
	// ensure they've been deleted.
	for _, namespace := range namespaceList.Items {
		namespaceName := namespace.GetName()

		configmapList, err := clientSet.ConfigMaps(namespaceName).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.NoError(t, checkCleanConfigMaps(configmapList, namespaceName))

		requireNoObjects(clientSet.Secrets(namespaceName).List(ctx, metav1.ListOptions{}))

		requireNoObjects(clientSet.Pods(namespaceName).List(ctx, metav1.ListOptions{}))
	}

	requireNoObjects(clientSet.ConfigV1Interface.Nodes().List(ctx, metav1.ListOptions{}))

	// ###########
	// END: "core"
	// ###########

	// #####################################
	// BEGIN: operator.openshift.io/v1alpha1
	// #####################################
	requireNoObjects(clientSet.ImageContentSourcePolicies().List(ctx, metav1.ListOptions{}))
	// #####################################
	// END: operator.openshift.io/v1alpha1
	// #####################################

	// #############################
	// BEGIN: config.openshift.io/v1
	// #############################
	requireNoObjects(clientSet.ConfigV1Interface.Images().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.ClusterVersions().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.FeatureGates().List(ctx, metav1.ListOptions{}))

	requireNoObjects(clientSet.ConfigV1Interface.Nodes().List(ctx, metav1.ListOptions{}))
	// ###########################
	// END: config.openshift.io/v1
	// ###########################
}

// cleanEnvironment is called at the end of the test to ensure that all the resources that were created during the test
// are removed ahead of the next test starting.
func CleanEnvironment(t *testing.T, clientSet *framework.ClientSet) {
	t.Helper()

	ctx := context.Background()

	var gracePeriod int64 = 0 //nolint:revive // This should be zero

	// Ensure that items are deleted immediately.
	deleteOpts := metav1.DeleteOptions{
		GracePeriodSeconds: &gracePeriod,
	}

	// ########################################
	// BEGIN: machineconfiguration.openshift.io
	// ########################################
	err := clientSet.ContainerRuntimeConfigs().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.ControllerConfigs().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	// KubeletConfigs must have their finalizers removed
	kcList, err := clientSet.KubeletConfigs().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, kc := range kcList.Items {
		if len(kc.Finalizers) > 0 {
			k := kc.DeepCopy()
			k.Finalizers = []string{}
			_, err := clientSet.KubeletConfigs().Update(ctx, k, metav1.UpdateOptions{})
			require.NoError(t, err)
		}
	}

	err = clientSet.KubeletConfigs().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.MachineConfigPools().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.MachineConfigs().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)
	// ######################################
	// END: machineconfiguration.openshift.io
	// ######################################

	// #############
	// BEGIN: "core"
	// #############
	namespaceList, err := clientSet.Namespaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// Iterate through each namespace for namespace-scoped objects so we can
	// delete them from all known namespaces.
	for _, namespace := range namespaceList.Items {
		namespaceName := namespace.GetName()

		err = clientSet.ConfigMaps(namespaceName).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
		require.NoError(t, err)

		err = clientSet.Secrets(namespaceName).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
		require.NoError(t, err)

		err = clientSet.Pods(namespaceName).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
		require.NoError(t, err)
	}

	err = clientSet.CoreV1Interface.Nodes().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)
	// ###########
	// END: "core"
	// ###########

	// #####################################
	// BEGIN: operator.openshift.io/v1alpha1
	// #####################################
	err = clientSet.ImageContentSourcePolicies().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)
	// #####################################
	// END: operator.openshift.io/v1alpha1
	// #####################################

	// #############################
	// BEGIN: config.openshift.io/v1
	// #############################
	err = clientSet.ConfigV1Interface.Images().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.ClusterVersions().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.FeatureGates().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.ConfigV1Interface.Nodes().DeleteCollection(ctx, deleteOpts, metav1.ListOptions{})
	require.NoError(t, err)
	// ###########################
	// END: config.openshift.io/v1
	// ###########################
}

type namespacedObject interface {
	GetNamespace() string
	GetName() string
}

func CreateObjects(t *testing.T, clientSet *framework.ClientSet, objs ...runtime.Object) {
	t.Helper()

	ctx := context.Background()

	for _, obj := range objs {
		require.NoError(t, createObject(ctx, clientSet, obj))
	}
}

func createObject(ctx context.Context, clientSet *framework.ClientSet, obj runtime.Object) error {
	switch tObj := obj.(type) {
	case *mcfgv1.MachineConfig:
		_, err := clientSet.MachineConfigs().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *mcfgv1.MachineConfigPool:
		_, err := clientSet.MachineConfigPools().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *mcfgv1.ControllerConfig:
		// Hack to get the pull secret working for the template controller
		o := tObj.DeepCopy()
		o.Spec.PullSecret = &corev1.ObjectReference{
			Name:      "pull-secret",
			Namespace: OpenshiftConfigNamespace,
		}

		_, err := clientSet.ControllerConfigs().Create(ctx, o, metav1.CreateOptions{})
		return err
	case *mcfgv1.ContainerRuntimeConfig:
		_, err := clientSet.ContainerRuntimeConfigs().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *mcfgv1.KubeletConfig:
		_, err := clientSet.KubeletConfigs().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *corev1.Node:
		_, err := clientSet.CoreV1Interface.Nodes().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *apioperatorsv1alpha1.ImageContentSourcePolicy:
		_, err := clientSet.ImageContentSourcePolicies().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *configv1.Image:
		_, err := clientSet.ConfigV1Interface.Images().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *configv1.FeatureGate:
		_, err := clientSet.FeatureGates().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *configv1.Node:
		_, err := clientSet.ConfigV1Interface.Nodes().Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case namespacedObject:
		return createNamespacedObject(ctx, clientSet, tObj)
	default:
		return fmt.Errorf("Unknown object type %T", obj)
	}
}

// If we have a namespaced object, we need to create the namespace before we can create the object.
func createNamespacedObject(ctx context.Context, clientSet *framework.ClientSet, obj namespacedObject) error {
	namespaceName := obj.GetNamespace()

	if namespaceName == "" {
		return fmt.Errorf("could not create namespaced object (%s); no namespace provided", obj.GetName())
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}

	_, err := clientSet.Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
	// It's OK if the namespace already exists, so we don't want to error on that.
	if err != nil && !k8sErrors.IsAlreadyExists(err) {
		return err
	}

	switch tObj := obj.(type) {
	case *corev1.ConfigMap:
		_, err := clientSet.ConfigMaps(namespaceName).Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *corev1.Secret:
		_, err := clientSet.Secrets(namespaceName).Create(ctx, tObj, metav1.CreateOptions{})
		return err
	case *corev1.Pod:
		_, err := clientSet.Pods(namespaceName).Create(ctx, tObj, metav1.CreateOptions{})
		return err
	default:
		return fmt.Errorf("unknown namespaced object type %T", obj)
	}
}
