package e2e_bootstrap_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/machine-config-operator/internal/clients"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/bootstrap"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	bootstrapTestName        = "BootstrapTest"
	templatesDir             = "../../templates"
	bootstrapTestDataDir     = "../../pkg/controller/bootstrap/testdata/bootstrap"
	imagesFile               = "../../install/image-references"
	openshiftConfigNamespace = "openshift-config"
	componentNamespace       = "openshift-machine-config-operator"
	pollInterval             = 200 * time.Millisecond
	pollTimeout              = 30 * time.Second
)

var (
	corev1GroupVersion = schema.GroupVersion{
		Group:   "",
		Version: "v1",
	}
)

type fixture struct {
	stop         func()
	manifestsDir string
}

func TestE2EBootstrap(t *testing.T) {
	ctx := context.Background()

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "install"),
			filepath.Join("..", "..", "manifests", "controllerconfig.crd.yaml"),
			filepath.Join("..", "..", "vendor", "github.com", "openshift", "api", "config", "v1"),
			filepath.Join("..", "..", "vendor", "github.com", "openshift", "api", "operator", "v1alpha1"),
		},
	}

	configv1.Install(scheme.Scheme)
	mcfgv1.Install(scheme.Scheme)
	apioperatorsv1alpha1.Install(scheme.Scheme)

	baseTestManifests := loadBaseTestManifests(t)

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, testEnv.Stop())
	}()

	clientSet := framework.NewClientSetFromConfig(cfg)

	_, err = clientSet.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: openshiftConfigNamespace,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	testCases := []struct {
		name             string
		manifests        [][]byte
		waitForMasterMCs []string
		waitForWorkerMCs []string
	}{
		{
			name:             "With no additional manifests",
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries"},
		},
		{
			name: "With a featuregate manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: FeatureGate
metadata:
  name: cluster
spec:
  featureSet: TechPreviewNoUpgrade`),
			},
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "98-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "98-worker-generated-kubelet"},
		},
		{
			name: "With a featuregate manifest and master kubelet config manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: FeatureGate
metadata:
  name: cluster
spec:
  featureSet: TechPreviewNoUpgrade`),
				[]byte(`apiVersion: machineconfiguration.openshift.io/v1
kind: KubeletConfig
metadata:
  name: master-kubelet-config
spec:
  machineConfigPoolSelector:
    matchLabels:
      pools.operator.machineconfiguration.openshift.io/master: ""
  kubeletConfig:
    podsPerCore: 10
    maxPods: 250
    systemReserved:
      cpu: 1000m
      memory: 500Mi
    kubeReserved:
      cpu: 1000m
      memory: 500Mi
`),
			},
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "99-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries"},
		},
		{
			name: "With a worker kubelet config manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: machineconfiguration.openshift.io/v1
kind: KubeletConfig
metadata:
  name: worker-kubelet-config
spec:
  machineConfigPoolSelector:
    matchLabels:
      pools.operator.machineconfiguration.openshift.io/worker: ""
  kubeletConfig:
    podsPerCore: 10
    maxPods: 250
    systemReserved:
      cpu: 1000m
      memory: 500Mi
    kubeReserved:
      cpu: 1000m
      memory: 500Mi
`),
			},
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "99-worker-generated-kubelet"},
		},
		{
			name: "With a container runtime config",
			manifests: [][]byte{
				[]byte(`apiVersion: machineconfiguration.openshift.io/v1
kind: ContainerRuntimeConfig
metadata:
  name: cr-pid-limit
spec:
  machineConfigPoolSelector:
    matchLabels:
      pools.operator.machineconfiguration.openshift.io/master: ""
  containerRuntimeConfig:
    pidsLimit: 100000
`),
			},
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "99-master-generated-containerruntime"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]runtime.Object{}, baseTestManifests...)
			objs = append(objs, loadRawManifests(t, tc.manifests)...)

			fixture := newTestFixture(t, cfg, objs)
			// Defer stop after cleanup so that the cleanup happens after the stop (defer unwrapping order)
			defer cleanEnvironment(t, clientSet)
			defer fixture.stop()

			// Fetch the controller rendered configurations
			controllerRenderedMasterConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "master", tc.waitForMasterMCs...)
			require.NoError(t, err)
			t.Logf("Controller rendered master config as %q", controllerRenderedMasterConfigName)

			controllerRenderedWorkerConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "worker", tc.waitForWorkerMCs...)
			require.NoError(t, err)
			t.Logf("Controller rendered worker config as %q", controllerRenderedWorkerConfigName)

			// Set up the output and input directories
			destDir, err := ioutil.TempDir("", "controller-bootstrap")
			require.NoError(t, err)
			defer os.RemoveAll(destDir)

			srcDir, err := ioutil.TempDir("", "controller-bootstrap-source")
			require.NoError(t, err)
			defer os.RemoveAll(srcDir)

			// Ensure all the manifests are in the input directory
			err = copyDir(bootstrapTestDataDir, srcDir)
			require.NoError(t, err)

			for id, manifest := range tc.manifests {
				name := fmt.Sprintf("manifest-%d.yaml", id)
				path := filepath.Join(srcDir, name)
				err := os.WriteFile(path, manifest, 0644)
				require.NoError(t, err)
			}

			// Run the bootstrap
			bootstrapper := bootstrap.New(templatesDir, srcDir, filepath.Join(bootstrapTestDataDir, "/machineconfigcontroller-pull-secret"))
			err = bootstrapper.Run(destDir)
			require.NoError(t, err)

			// Compare the rendered configs
			compareRenderedConfigPool(t, clientSet, destDir, "master", controllerRenderedMasterConfigName)
			compareRenderedConfigPool(t, clientSet, destDir, "worker", controllerRenderedWorkerConfigName)

		})
	}
}

func compareRenderedConfigPool(t *testing.T, clientSet *framework.ClientSet, destDir, poolName, controllerRenderedConfigName string) {
	paths, err := filepath.Glob(filepath.Join(destDir, "machine-configs", fmt.Sprintf("rendered-%s-*.yaml", poolName)))
	require.NoError(t, err)
	require.Len(t, paths, 1)

	bootstrapRenderedConfigFilePath := paths[0]
	bootstrapRenderedConfigName := strings.TrimSuffix(filepath.Base(bootstrapRenderedConfigFilePath), ".yaml")
	t.Logf("Bootstrap rendered %s config as %q", poolName, bootstrapRenderedConfigName)

	if controllerRenderedConfigName != bootstrapRenderedConfigName {
		t.Errorf("Expected rendered %s configurations to match: got bootstrap config %q, got controller config %q", poolName, bootstrapRenderedConfigName, controllerRenderedConfigName)

		controllerMC, err := clientSet.MachineConfigs().Get(context.Background(), controllerRenderedConfigName, metav1.GetOptions{})
		assert.NoError(t, err)
		controllerMCYAML, err := yaml.Marshal(controllerMC)
		assert.NoError(t, err)
		t.Logf("Controller rendered %q:\n%s", controllerRenderedConfigName, controllerMCYAML)

		bootstrapMCYAML, err := os.ReadFile(bootstrapRenderedConfigFilePath)
		assert.NoError(t, err)
		t.Logf("Bootstrap rendered %q:\n%s", bootstrapRenderedConfigName, bootstrapMCYAML)
	}
}

func newTestFixture(t *testing.T, cfg *rest.Config, objs []runtime.Object) *fixture {
	ctx, stop := context.WithCancel(context.Background())
	cb := clients.BuilderFromConfig(cfg)
	ctrlctx := ctrlcommon.CreateControllerContext(cb, ctx.Done(), bootstrapTestName)

	clientSet := framework.NewClientSetFromConfig(cfg)

	// Ensure the environment has been cleaned, then create this tests objects
	checkCleanEnvironment(t, clientSet)
	createObjects(t, clientSet, objs...)
	createClusterVersion(t, clientSet, objs...)

	controllers := createControllers(ctrlctx)

	// Start the shared factory informers that you need to use in your controller
	ctrlctx.InformerFactory.Start(ctrlctx.Stop)
	ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)

	close(ctrlctx.InformersStarted)

	for _, c := range controllers {
		go c.Run(2, ctrlctx.Stop)
	}

	return &fixture{
		stop: stop,
	}
}

// Pretty much copied verbatim from cmd/machine-config-controller/start.go
func createControllers(ctx *ctrlcommon.ControllerContext) []ctrlcommon.Controller {
	var controllers []ctrlcommon.Controller

	controllers = append(controllers,
		// Our primary MCs come from here
		template.New(
			templatesDir,
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.OpenShiftConfigKubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctx.ConfigInformerFactory.Config().V1().FeatureGates(),
			ctx.ClientBuilder.KubeClientOrDie("template-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("template-controller"),
		),
		// Add all "sub-renderers here"
		kubeletconfig.New(
			templatesDir,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().KubeletConfigs(),
			ctx.ConfigInformerFactory.Config().V1().FeatureGates(),
			ctx.ConfigInformerFactory.Config().V1().APIServers(),
			ctx.ClientBuilder.KubeClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("kubelet-config-controller"),
		),
		containerruntimeconfig.New(
			templatesDir,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.ConfigInformerFactory.Config().V1().Images(),
			ctx.OperatorInformerFactory.Operator().V1alpha1().ImageContentSourcePolicies(),
			ctx.ConfigInformerFactory.Config().V1().ClusterVersions(),
			ctx.ClientBuilder.KubeClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("container-runtime-config-controller"),
		),
		// The renderer creates "rendered" MCs from the MC fragments generated by
		// the above sub-controllers, which are then consumed by the node controller
		render.New(
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.ClientBuilder.KubeClientOrDie("render-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("render-controller"),
			ctx.ClientBuilder.ImageClientOrDie("render-controller"),
		),
		// The node controller consumes data written by the above
		node.New(
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.ConfigInformerFactory.Config().V1().Schedulers(),
			ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
		),
	)

	return controllers
}

func createObjects(t *testing.T, clientSet *framework.ClientSet, objs ...runtime.Object) {
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
		default:
			t.Errorf("Unknown object type %T", obj)
		}
	}
}

// createClusterVersion creates a ClusterVersion with the correct status to allow the
// container runtime config controller to create the registry configuration
func createClusterVersion(t *testing.T, clientSet *framework.ClientSet, objs ...runtime.Object) {
	ctx := context.Background()
	var controllerConfig *mcfgv1.ControllerConfig
	for _, obj := range objs {
		if cc, ok := obj.(*mcfgv1.ControllerConfig); ok {
			controllerConfig = cc
			break
		}
	}
	require.NotNil(t, controllerConfig, "Did not find controller config in base manifests")

	cv := &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
	}
	cv, err := clientSet.ClusterVersions().Create(ctx, cv, metav1.CreateOptions{})
	require.NoError(t, err)

	cv.Status.Desired.Image = controllerConfig.Spec.ReleaseImage
	cv, err = clientSet.ClusterVersions().UpdateStatus(ctx, cv, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, cv.Status.Desired.Image)
}

// loadBaseTestManifests loads all of the yaml files in the directory
// and decodes them into runtime objects.
func loadBaseTestManifests(t *testing.T) []runtime.Object {
	fileInfos, err := os.ReadDir(bootstrapTestDataDir)
	require.NoError(t, err)

	rawObjs := [][]byte{}
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(bootstrapTestDataDir, fileInfo.Name()))
		require.NoError(t, err)
		rawObjs = append(rawObjs, data)
	}

	return loadRawManifests(t, rawObjs)
}

func loadRawManifests(t *testing.T, rawObjs [][]byte) []runtime.Object {
	codecFactory := serializer.NewCodecFactory(scheme.Scheme)
	decoder := codecFactory.UniversalDecoder(corev1GroupVersion, mcfgv1.GroupVersion, apioperatorsv1alpha1.GroupVersion, configv1.GroupVersion)

	objs := []runtime.Object{}
	for _, raw := range rawObjs {
		obj, err := runtime.Decode(decoder, raw)
		require.NoError(t, err)

		objs = append(objs, obj)
	}

	return objs
}

// checkCleanEnvironment checks that all of the resource types that are to be used in this test currently have no items.
// This ensures that no atifacts from previous test runs are interfering with the current test.
func checkCleanEnvironment(t *testing.T, clientSet *framework.ClientSet) {
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
	// ###########################
	// END: config.openshift.io/v1
	// ###########################
}

// cleanEnvironment is called at the end of the test to ensure that all the resources that were created during the test
// are removed ahead of the next test starting.
func cleanEnvironment(t *testing.T, clientSet *framework.ClientSet) {
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
	// ###########################
	// END: config.openshift.io/v1
	// ###########################
}

// copyDir copies the contents of one directory to another,
// both directories must exist, does not copy recursively
func copyDir(src string, dest string) error {
	if strings.HasPrefix(dest, src) {
		return fmt.Errorf("Cannot copy a folder into the folder itself!")
	}

	f, err := os.Open(src)
	if err != nil {
		return err
	}

	file, err := f.Stat()
	if err != nil {
		return err
	}
	if !file.IsDir() {
		return fmt.Errorf("Source " + file.Name() + " is not a directory!")
	}

	files, err := ioutil.ReadDir(src)
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		content, err := ioutil.ReadFile(src + "/" + f.Name())
		if err != nil {
			return err
		}

		err = ioutil.WriteFile(dest+"/"+f.Name(), content, 0755)
		if err != nil {
			return err
		}
	}

	return nil
}
