package e2e_bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"

	"github.com/ghodss/yaml"
	configv1 "github.com/openshift/api/config/v1"
	_ "github.com/openshift/api/config/v1/zz_generated.crd-manifests"
	configv1alpha1 "github.com/openshift/api/config/v1alpha1"
	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	_ "github.com/openshift/api/operator/v1alpha1/zz_generated.crd-manifests"
	featuregatescontroller "github.com/openshift/api/payload-command/render"

	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/bootstrap"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

const (
	bootstrapTestName    = "bootstrap-test"
	templatesDir         = "../../templates"
	bootstrapTestDataDir = "../../pkg/controller/bootstrap/testdata/bootstrap"
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

	testEnv := framework.NewTestEnv(t)

	configv1.Install(scheme.Scheme)
	configv1alpha1.Install(scheme.Scheme)
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
			Name: framework.OpenshiftConfigNamespace,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = clientSet.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: bootstrapTestName,
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
			name: "With a node config manifest empty spec",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster`),
			},
			// "CgroupMode" field in the nodes.config resource is empty
			// Internally it gets updated to "v2" explicitly
			// Hence, 97-{master/worker}-generated-kubelet are expected
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "97-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "97-worker-generated-kubelet"},
		},
		{
			name: "With a node config manifest empty \"cgroupMode\"",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster
spec:
  workerLatencyProfile: MediumUpdateAverageReaction`),
			},
			// "CgroupMode" field in the nodes.config resource is empty
			// Internally it gets updated to "v2" explicitly
			// Hence, 97-{master/worker}-generated-kubelet are expected
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "97-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "97-worker-generated-kubelet"},
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
			name: "With a featuregate manifest and a config node manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: FeatureGate
metadata:
  name: cluster
spec:
  featureSet: TechPreviewNoUpgrade`),
				[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster
spec:
  cgroupMode: "v2"`),
			},
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "98-master-generated-kubelet", "97-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "98-worker-generated-kubelet", "97-worker-generated-kubelet"},
		},
		{
			name: "With a config node manifest and without a featuregate manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster
spec:
  cgroupMode: "v2"`),
			},
			// As the CGroupsV2 feature is GA, 97-{master/worker}-generated-kubelet mcs are expected even without a Techpreview featuregate
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "97-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "97-worker-generated-kubelet"},
		},
		{
			name: "With a node config manifest and a master kubelet config manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster
spec:
  workerLatencyProfile: MediumUpdateAverageReaction
  cgroupMode: "v2"`),
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
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "99-master-generated-kubelet", "97-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "97-worker-generated-kubelet"},
		},
		{
			name: "With a node config manifest and a worker kubelet config manifest",
			manifests: [][]byte{
				[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster
spec:
  workerLatencyProfile: MediumUpdateAverageReaction`),
				[]byte(`apiVersion: machineconfiguration.openshift.io/v1
kind: KubeletConfig
metadata:
  name: master-kubelet-config
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
			// 97-{master/worker}-generated-kubelet are expected to be created as the empty "cgroupMode"
			// internally translates to "v2"
			waitForMasterMCs: []string{"99-master-ssh", "99-master-generated-registries", "97-master-generated-kubelet"},
			waitForWorkerMCs: []string{"99-worker-ssh", "99-worker-generated-registries", "99-worker-generated-kubelet", "97-worker-generated-kubelet"},
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

			// Only add this node config if one doesn't already exist.
			// If two are present, the latter one will overwrite the former one.
			if !containsGVK(objs, configv1.SchemeGroupVersion.WithKind("Node")) {
				nodeConfigManifest := [][]byte{
					[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster`),
				}
				objs = append(objs, loadRawManifests(t, nodeConfigManifest)...)
			}

			fixture := newTestFixture(t, cfg, objs)
			// Defer stop after cleanup so that the cleanup happens after the stop (defer unwrapping order)
			defer framework.CleanEnvironment(t, clientSet)
			defer fixture.stop()

			// Fetch the controller rendered configurations
			controllerRenderedMasterConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "master", tc.waitForMasterMCs...)
			require.NoError(t, err)
			t.Logf("Controller rendered master config as %q", controllerRenderedMasterConfigName)

			controllerRenderedWorkerConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "worker", tc.waitForWorkerMCs...)
			require.NoError(t, err)
			t.Logf("Controller rendered worker config as %q", controllerRenderedWorkerConfigName)

			// Set up the output and input directories
			destDir, err := os.MkdirTemp("", "controller-bootstrap")
			require.NoError(t, err)
			defer os.RemoveAll(destDir)

			srcDir, err := os.MkdirTemp("", "controller-bootstrap-source")
			require.NoError(t, err)
			defer os.RemoveAll(srcDir)

			for id, obj := range objs {
				manifest, err := yaml.Marshal(obj)
				require.NoError(t, err)

				name := fmt.Sprintf("manifest-%d.yaml", id)
				path := filepath.Join(srcDir, name)
				err = os.WriteFile(path, manifest, 0644)
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

func TestNodeSizingEnabled(t *testing.T) {
	ctx := context.Background()

	testEnv := framework.NewTestEnv(t)

	configv1.Install(scheme.Scheme)
	configv1alpha1.Install(scheme.Scheme)
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
			Name: framework.OpenshiftConfigNamespace,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = clientSet.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: bootstrapTestName,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	objs := append([]runtime.Object{}, baseTestManifests...)

	// Add node config
	nodeConfigManifest := [][]byte{
		[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster`),
	}
	objs = append(objs, loadRawManifests(t, nodeConfigManifest)...)

	fixture := newTestFixture(t, cfg, objs)
	defer framework.CleanEnvironment(t, clientSet)
	defer fixture.stop()

	// Fetch the controller rendered configurations
	controllerRenderedMasterConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "master", []string{"99-master-ssh", "99-master-generated-registries"}...)
	require.NoError(t, err)
	t.Logf("Controller rendered master config as %q", controllerRenderedMasterConfigName)

	controllerRenderedWorkerConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "worker", []string{"99-worker-ssh", "99-worker-generated-registries"}...)
	require.NoError(t, err)
	t.Logf("Controller rendered worker config as %q", controllerRenderedWorkerConfigName)

	// Verify node sizing enabled file for master
	verifyNodeSizingEnabled(t, clientSet, controllerRenderedMasterConfigName)

	// Verify node sizing enabled file for worker
	verifyNodeSizingEnabled(t, clientSet, controllerRenderedWorkerConfigName)
}

func verifyNodeSizingEnabled(t *testing.T, clientSet *framework.ClientSet, renderedConfigName string) {
	controllerMC, err := clientSet.MachineConfigs().Get(context.Background(), renderedConfigName, metav1.GetOptions{})
	require.NoError(t, err)

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(controllerMC.Spec.Config.Raw)
	require.NoError(t, err)

	// Find the node sizing enabled file
	var foundFile bool
	for _, file := range ignCfg.Storage.Files {
		if file.Path == "/etc/node-sizing-enabled.env" {
			foundFile = true

			// Decode the file contents
			contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
			require.NoError(t, err, "Failed to decode node-sizing-enabled.env file contents")

			contentsStr := string(contents)
			require.Contains(t, contentsStr, "NODE_SIZING_ENABLED=true", "Expected /etc/node-sizing-enabled.env to contain NODE_SIZING_ENABLED=true in machine config %s", renderedConfigName)
			break
		}
	}

	require.True(t, foundFile, "Expected to find /etc/node-sizing-enabled.env in machine config %s", renderedConfigName)
}

func compareRenderedConfigPool(t *testing.T, clientSet *framework.ClientSet, destDir, poolName, controllerRenderedConfigName string) {
	paths, err := filepath.Glob(filepath.Join(destDir, "machine-configs", fmt.Sprintf("rendered-%s-*.yaml", poolName)))
	require.NoError(t, err)
	require.Len(t, paths, 1)

	bootstrapRenderedConfigFilePath := paths[0]
	bootstrapRenderedConfigName := strings.TrimSuffix(filepath.Base(bootstrapRenderedConfigFilePath), ".yaml")
	t.Logf("Bootstrap rendered %s config as %q", poolName, bootstrapRenderedConfigName)

	controllerMC, err := clientSet.MachineConfigs().Get(context.Background(), controllerRenderedConfigName, metav1.GetOptions{})
	require.Nil(t, err)
	outIgn := ign3types.Config{}
	err = json.Unmarshal(controllerMC.Spec.Config.Raw, &outIgn)

	for _, file := range outIgn.Storage.Files {
		require.False(t, file.Path == "/etc/kubernetes/kubelet-ca.crt")
		require.False(t, file.Path == "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem")
	}
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
	ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)

	clientSet := framework.NewClientSetFromConfig(cfg)

	// Ensure the environment has been cleaned, then create this tests objects
	framework.CheckCleanEnvironment(t, clientSet)
	framework.CreateObjects(t, clientSet, objs...)
	createClusterVersion(t, clientSet, objs...)
	ensureFeatureGate(t, clientSet, objs...)

	controllers := createControllers(ctrlctx)

	// Start the shared factory informers that you need to use in your controller
	ctrlctx.InformerFactory.Start(ctrlctx.Stop)
	ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)

	err := ctrlctx.FeatureGatesHandler.Connect(ctx)
	require.NoError(t, err, "FeatureGates should be available before proceeding")

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
			ctx.ConfigInformerFactory.Config().V1().APIServers(),
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
			ctx.ConfigInformerFactory.Config().V1().Nodes(),
			ctx.ConfigInformerFactory.Config().V1().APIServers(),
			ctx.ClientBuilder.KubeClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("kubelet-config-controller"),
			ctx.FeatureGatesHandler,
		),
		containerruntimeconfig.New(
			templatesDir,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.ConfigInformerFactory.Config().V1().Images(),
			ctx.ConfigInformerFactory.Config().V1().ImageDigestMirrorSets(),
			ctx.ConfigInformerFactory.Config().V1().ImageTagMirrorSets(),
			ctx.ConfigInformerFactory,
			ctx.OperatorInformerFactory.Operator().V1alpha1().ImageContentSourcePolicies(),
			ctx.ConfigInformerFactory.Config().V1().ClusterVersions(),
			ctx.ClientBuilder.KubeClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("container-runtime-config-controller"),
			ctx.FeatureGatesHandler,
		),
		// The renderer creates "rendered" MCs from the MC fragments generated by
		// the above sub-controllers, which are then consumed by the node controller
		render.New(
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().KubeletConfigs(),
			ctx.OperatorInformerFactory.Operator().V1().MachineConfigurations(),
			ctx.ClientBuilder.KubeClientOrDie("render-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("render-controller"),
			ctx.FeatureGatesHandler,
		),
		// The node controller consumes data written by the above
		node.New(
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.KubeInformerFactory.Core().V1().Pods(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineOSConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineOSBuilds(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigNodes(),
			ctx.ConfigInformerFactory.Config().V1().Schedulers(),
			ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
			ctx.FeatureGatesHandler,
		),
	)

	return controllers
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

	cv.Status.Desired.Version = version.ReleaseVersion
	cv.Status.Desired.Image = controllerConfig.Spec.ReleaseImage
	cv, err = clientSet.ClusterVersions().UpdateStatus(ctx, cv, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, cv.Status.Desired.Image)
}

// ensureFeatureGate ensures that the cluster contains a feature gate with the
// correct status to allow the controllers to proceed.
func ensureFeatureGate(t *testing.T, clientSet *framework.ClientSet, objs ...runtime.Object) {
	ctx := context.Background()

	var controllerConfig *mcfgv1.ControllerConfig
	for _, obj := range objs {
		if cc, ok := obj.(*mcfgv1.ControllerConfig); ok {
			controllerConfig = cc
			break
		}
	}
	require.NotNil(t, controllerConfig, "Did not find controller config in base manifests")

	currentFg, err := clientSet.FeatureGates().Get(ctx, "cluster", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		require.NoError(t, err)
	} else {
		t.Fatal("FeatureGate cluster not found, bootstrap data should contain at least 1 FeatureGate")
	}

	currentFeatureSet := currentFg.Spec.FeatureSet

	SelfManaged := features.ClusterProfileName("include.release.openshift.io/self-managed-high-availability")
	if err != nil {
		t.Fatalf("Error retrieving current feature gates: %v", err)
	}
	featureGateStatus, err := features.FeatureSets(features.ClusterProfileName(SelfManaged), currentFeatureSet)

	require.NoError(t, err)
	currentDetails := featuregatescontroller.FeaturesGateDetailsFromFeatureSets(featureGateStatus, controllerConfig.Spec.ReleaseImage)

	rawDetails := *currentDetails
	rawDetails.Version = version.ReleaseVersion

	currentFg.Status = configv1.FeatureGateStatus{
		FeatureGates: []configv1.FeatureGateDetails{*currentDetails, rawDetails},
	}

	// Update the feature gate with the current controllerconfig image.
	_, err = clientSet.FeatureGates().UpdateStatus(ctx, currentFg, metav1.UpdateOptions{})
	require.NoError(t, err)

	// For some reason API calls aren't populating this? But it's needed for the bootstrap render.
	currentFg.SetGroupVersionKind(configv1.SchemeGroupVersion.WithKind("FeatureGate"))

	// Make sure that the objects are up to date with the FG
	// else the bootstrap render will not work.
	for i, obj := range objs {
		if _, ok := obj.(*configv1.FeatureGate); ok {
			objs[i] = currentFg
		}
	}
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
	decoder := codecFactory.UniversalDecoder(corev1GroupVersion, mcfgv1.GroupVersion, apioperatorsv1alpha1.GroupVersion, configv1alpha1.GroupVersion, configv1.GroupVersion)

	objs := []runtime.Object{}
	for _, raw := range rawObjs {
		obj, err := runtime.Decode(decoder, raw)
		require.NoError(t, err)

		objs = append(objs, obj)
	}

	return objs
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
		return fmt.Errorf("Source %v is not a directory!", file.Name())
	}

	files, err := ctrlcommon.ReadDir(src)
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		content, err := os.ReadFile(src + "/" + f.Name())
		if err != nil {
			return err
		}

		err = os.WriteFile(dest+"/"+f.Name(), content, 0755)
		if err != nil {
			return err
		}
	}

	return nil
}

func containsGVK(objs []runtime.Object, gvk schema.GroupVersionKind) bool {
	for _, obj := range objs {
		if obj.GetObjectKind().GroupVersionKind() == gvk {
			return true
		}
	}
	return false
}
