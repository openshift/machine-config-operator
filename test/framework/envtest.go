package framework

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	goruntime "runtime"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	configv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
)

const (
	openshiftConfigNamespace string = "openshift-config"
	kubebuilderPath          string = "/tmp/kubebuilder"
	envTestPath              string = kubebuilderPath + "/bin"

	// TODO: Figure out how to obtain this value programmatically so we don't
	// have to remember to increment it.
	k8sVersion string = "1.22.1"
)

func fetchTools() error {
	if _, ok := os.LookupEnv("SKIP_FETCH_TOOLS"); ok {
		fmt.Println("SKIP_FETCH_TOOLS set, skipping!")
		return nil
	}

	toolsArchiveName := fmt.Sprintf("kubebuilder-tools-%s-%s-%s.tar.gz", k8sVersion, goruntime.GOOS, goruntime.GOARCH)
	downloadURL := fmt.Sprintf("https://storage.googleapis.com/kubebuilder-tools/%s", toolsArchiveName)
	toolsArchiveDownloadPath := filepath.Join("/tmp/kubebuilder", toolsArchiveName)

	if err := os.MkdirAll(envTestPath, 0755); err != nil {
		return fmt.Errorf("could not mkdir: %w", err)
	}

	// Check if we have the archive on disk. If not, we should retrieve it.
	if _, err := os.Stat(toolsArchiveDownloadPath); os.IsNotExist(err) {
		// While it is possible to write this and the tar extract below in pure Go, I
		// opted to shell out to preexisting tools for brevity.
		cmd := exec.Command("curl", "-fsL", downloadURL, "-o", toolsArchiveDownloadPath)

		fmt.Println("Executing", cmd)

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("could not download tools archive: %w", err)
		}
	} else {
		fmt.Println("Found preexisting tools archive", toolsArchiveDownloadPath)
	}

	cmd := exec.Command("tar", "-zvxf", toolsArchiveDownloadPath, "-C", "/tmp/")

	fmt.Println("Executing", cmd)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not extract tools archive: %w", err)
	}

	return nil
}

func NewTestEnv() *envtest.Environment {
	if err := fetchTools(); err != nil {
		panic(err)
	}

	home := os.Getenv("HOME")
	if home == "/" || home == "" {
		os.Setenv("HOME", kubebuilderPath)
	}

	return &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "install"),
			filepath.Join("..", "..", "manifests", "controllerconfig.crd.yaml"),
			filepath.Join("..", "..", "vendor", "github.com", "openshift", "api", "config", "v1"),
			filepath.Join("..", "..", "vendor", "github.com", "openshift", "api", "operator", "v1alpha1"),
		},
		BinaryAssetsDirectory: envTestPath,
	}
}

// checkCleanEnvironment checks that all of the resource types that are to be used in this test currently have no items.
// This ensures that no atifacts from previous test runs are interfering with the current test.
func CheckCleanEnvironment(t *testing.T, clientSet *ClientSet) {
	t.Helper()

	ctx := context.Background()

	// ########################################
	// BEGIN: machineconfiguration.openshift.io
	// ########################################
	crcList, err := clientSet.ContainerRuntimeConfigs().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crcList.Items, 0)

	ccList, err := clientSet.ControllerConfigs().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, ccList.Items, 0)

	kcList, err := clientSet.KubeletConfigs().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, kcList.Items, 0)

	mcpList, err := clientSet.MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, mcpList.Items, 0)

	mcList, err := clientSet.MachineConfigs().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, mcList.Items, 0)
	// ######################################
	// END: machineconfiguration.openshift.io
	// ######################################

	// #############
	// BEGIN: "core"
	// #############
	secretList, err := clientSet.Secrets(openshiftConfigNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, secretList.Items, 0)
	// ###########
	// END: "core"
	// ###########

	// #####################################
	// BEGIN: operator.openshift.io/v1alpha1
	// #####################################
	icspList, err := clientSet.ImageContentSourcePolicies().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, icspList.Items, 0)
	// #####################################
	// END: operator.openshift.io/v1alpha1
	// #####################################

	// #############################
	// BEGIN: config.openshift.io/v1
	// #############################
	imagesList, err := clientSet.ConfigV1Interface.Images().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, imagesList.Items, 0)

	clusterVersionList, err := clientSet.ClusterVersions().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, clusterVersionList.Items, 0)

	featureGateList, err := clientSet.FeatureGates().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, featureGateList.Items, 0)

	nodeConfigList, err := clientSet.ConfigV1Interface.Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, nodeConfigList.Items, 0)
	// ###########################
	// END: config.openshift.io/v1
	// ###########################
}

// cleanEnvironment is called at the end of the test to ensure that all the resources that were created during the test
// are removed ahead of the next test starting.
func CleanEnvironment(t *testing.T, clientSet *ClientSet) {
	t.Helper()

	ctx := context.Background()

	// ########################################
	// BEGIN: machineconfiguration.openshift.io
	// ########################################
	err := clientSet.ContainerRuntimeConfigs().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.ControllerConfigs().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
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

	err = clientSet.KubeletConfigs().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.MachineConfigPools().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.MachineConfigs().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)
	// ######################################
	// END: machineconfiguration.openshift.io
	// ######################################

	// #############
	// BEGIN: "core"
	// #############
	err = clientSet.Secrets(openshiftConfigNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)
	// ###########
	// END: "core"
	// ###########

	// #####################################
	// BEGIN: operator.openshift.io/v1alpha1
	// #####################################
	err = clientSet.ImageContentSourcePolicies().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)
	// #####################################
	// END: operator.openshift.io/v1alpha1
	// #####################################

	// #############################
	// BEGIN: config.openshift.io/v1
	// #############################
	err = clientSet.ConfigV1Interface.Images().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.ClusterVersions().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.FeatureGates().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	err = clientSet.ConfigV1Interface.Nodes().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)
	// ###########################
	// END: config.openshift.io/v1
	// ###########################
}

func CreateObjects(t *testing.T, clientSet *ClientSet, objs ...runtime.Object) {
	t.Helper()

	ctx := context.Background()

	for _, obj := range objs {
		switch tObj := obj.(type) {
		case *mcfgv1.MachineConfig:
			_, err := clientSet.MachineConfigs().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *mcfgv1.MachineConfigPool:
			_, err := clientSet.MachineConfigPools().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *mcfgv1.ControllerConfig:
			// Hack to get the pull secret working for the template controller
			o := tObj.DeepCopy()
			o.Spec.PullSecret = &corev1.ObjectReference{
				Name:      "pull-secret",
				Namespace: openshiftConfigNamespace,
			}

			_, err := clientSet.ControllerConfigs().Create(ctx, o, metav1.CreateOptions{})
			require.NoError(t, err)
		case *mcfgv1.ContainerRuntimeConfig:
			_, err := clientSet.ContainerRuntimeConfigs().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *mcfgv1.KubeletConfig:
			_, err := clientSet.KubeletConfigs().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *corev1.Secret:
			_, err := clientSet.Secrets(tObj.GetNamespace()).Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *apioperatorsv1alpha1.ImageContentSourcePolicy:
			_, err := clientSet.ImageContentSourcePolicies().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *configv1.Image:
			_, err := clientSet.ConfigV1Interface.Images().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *configv1.FeatureGate:
			_, err := clientSet.FeatureGates().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		case *configv1.Node:
			_, err := clientSet.ConfigV1Interface.Nodes().Create(ctx, tObj, metav1.CreateOptions{})
			require.NoError(t, err)
		default:
			t.Errorf("Unknown object type %T", obj)
		}
	}
}
