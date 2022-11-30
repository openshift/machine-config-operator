package operator

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"path/filepath"
	"testing"
	"time"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"

	configv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/envtest"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

const (
	testComponentName  string = "machine-config"
	testKubeletVersion string = "1.0.0"
	testReleaseVersion string = "release-version"
)

func TestOperatorIntegration(t *testing.T) {
	configv1.Install(scheme.Scheme)
	mcfgv1.Install(scheme.Scheme)
	apioperatorsv1alpha1.Install(scheme.Scheme)

	testCases := []*envtest.EnvTestCase{
		{
			Name:      "Happy Path",
			SetupFunc: setupOperator,
			TestFunc: func(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) {
				// Create a timeout for our polling operation.
				pollCtx, pollCancel := context.WithTimeout(ctx, 1*time.Minute)
				defer pollCancel()

				// Assert that the operator has become available.
				assertOperatorIsAvailable(pollCtx, t, clientSet)
			},
		},
	}

	envtest.RunEnvTestCases(t, testCases)
}

// Writes the image file that is ingested by the operator.
// TODO: Use more realistic names.
func writeImagesFile(tempDir string) error {
	img := Images{
		ReleaseVersion: testReleaseVersion,
		RenderConfigImages: RenderConfigImages{
			MachineConfigOperator:          "machine-config-operator-image",
			MachineOSContent:               "machine-os-content-image",
			BaseOSContainerImage:           "base-os-container-image",
			BaseOSExtensionsContainerImage: "base-os-extensions-container-image",
			KeepalivedBootstrap:            "keepalived-bootstrap",
			CorednsBootstrap:               "coredns-bootstrap",
			BaremetalRuntimeCfgBootstrap:   "baremetal-runtime-cfg-bootstrap",
			OauthProxy:                     "oauth-proxy",
		},
		ControllerConfigImages: ControllerConfigImages{
			InfraImage:          "infra-image",
			Keepalived:          "keepalived",
			Coredns:             "coredns",
			Haproxy:             "haproxy",
			BaremetalRuntimeCfg: "baremetal-runtime-cfg",
		},
	}

	outBytes, err := json.Marshal(img)
	if err != nil {
		return err
	}

	path := filepath.Join(tempDir, "images.json")

	return ioutil.WriteFile(path, outBytes, 0755)
}

// This function is used to ensure that the various objects set up by the
// operator reach the statuses expected by the operator.
func startTestEventHandler(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) {
	go func() {
		dsWatcher, err := clientSet.AppsV1Interface.DaemonSets(ctrlcommon.MCONamespace).Watch(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		deployWatcher, err := clientSet.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Watch(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		controllerCfgWatcher, err := clientSet.MachineconfigurationV1Interface.ControllerConfigs().Watch(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		for {
			select {
			case <-ctx.Done():
				return
			case dsEvent := <-dsWatcher.ResultChan():
				handleDaemonsetEvent(ctx, t, dsEvent, clientSet)
			case deployEvent := <-deployWatcher.ResultChan():
				handleDeployEvent(ctx, t, deployEvent, clientSet)
			case controllerCfgEvent := <-controllerCfgWatcher.ResultChan():
				handleControllerCfgEvent(ctx, t, controllerCfgEvent, clientSet)
			}
		}
	}()
}

// Sets up and starts the operator for testing.
func setupOperator(ctx context.Context, t *testing.T, clientSet *framework.ClientSet, ctrlctx *common.ControllerContext) {
	tempDir := t.TempDir()
	require.NoError(t, writeImagesFile(tempDir))

	t.Setenv("RELEASE_VERSION", testReleaseVersion)

	controller := New(
		common.MCONamespace,
		testComponentName,
		filepath.Join(tempDir, "images.json"),
		ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		ctrlctx.KubeNamespacedInformerFactory.Core().V1().ServiceAccounts(),
		ctrlctx.APIExtInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		ctrlctx.KubeNamespacedInformerFactory.Apps().V1().Deployments(),
		ctrlctx.KubeNamespacedInformerFactory.Apps().V1().DaemonSets(),
		ctrlctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoles(),
		ctrlctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		ctrlctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
		ctrlctx.KubeInformerFactory.Core().V1().ConfigMaps(),
		ctrlctx.ConfigInformerFactory.Config().V1().Infrastructures(),
		ctrlctx.ConfigInformerFactory.Config().V1().Networks(),
		ctrlctx.ConfigInformerFactory.Config().V1().Proxies(),
		ctrlctx.ConfigInformerFactory.Config().V1().DNSes(),
		ctrlctx.ClientBuilder.MachineConfigClientOrDie(testComponentName),
		ctrlctx.ClientBuilder.KubeClientOrDie(testComponentName),
		ctrlctx.ClientBuilder.APIExtClientOrDie(testComponentName),
		ctrlctx.ClientBuilder.ConfigClientOrDie(testComponentName),
		ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
		ctrlctx.KubeInformerFactory.Core().V1().Nodes(),
		ctrlctx.KubeMAOSharedInformer.Core().V1().Secrets(),
	)

	ctrlctx.NamespacedInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.APIExtInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
	ctrlctx.KubeMAOSharedInformer.Start(ctrlctx.Stop)
	close(ctrlctx.InformersStarted)

	go controller.Run(2, ctrlctx.Stop)

	createKubeObjs(ctx, t, clientSet)

	// Start the event handler which ensures that the objects created by the operator reach the status they need to run.
	startTestEventHandler(ctx, t, clientSet)
}

// Polls until the operator is available.
func assertOperatorIsAvailable(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) {
	t.Helper()

	helpers.AssertConditionsAreReached(ctx, t, []wait.ConditionWithContextFunc{
		// Ensures that the operator is available.
		func(ctx context.Context) (bool, error) {
			co, err := clientSet.ConfigV1Interface.ClusterOperators().Get(ctx, testComponentName, metav1.GetOptions{})
			// If the machine-config cluster operator isn't present yet, we can't
			// verify that it's in the correct state. So return early.
			if k8sErrors.IsNotFound(err) {
				return false, nil
			}

			// If we've encountered any other error, fail the check.
			if err != nil && !k8sErrors.IsNotFound(err) {
				return false, err
			}

			return operatorIsAvailable(co), nil
		},
	})
}

// Examines the operator conditions to determine it is available.
func operatorIsAvailable(co *configv1.ClusterOperator) bool {
	for _, condition := range co.Status.Conditions {
		if condition.Type == configv1.OperatorDegraded && condition.Status == configv1.ConditionTrue {
			break
		}

		if condition.Type == configv1.OperatorProgressing && condition.Status == configv1.ConditionTrue {
			break
		}

		if condition.Type == configv1.OperatorUpgradeable && condition.Status == configv1.ConditionFalse {
			break
		}

		if condition.Type == configv1.OperatorAvailable && condition.Status == configv1.ConditionTrue {
			return true
		}
	}

	return false
}

// Simulates that the controller config has run.
func handleControllerCfgEvent(ctx context.Context, t *testing.T, event watch.Event, clientSet *framework.ClientSet) {
	if event.Type != watch.Added {
		return
	}

	controllerCfg := event.Object.(*mcfgv1.ControllerConfig)
	conditions := []*mcfgv1.ControllerConfigStatusCondition{
		mcfgv1.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerCompleted, corev1.ConditionTrue, "", ""),
		mcfgv1.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerRunning, corev1.ConditionFalse, "", ""),
		mcfgv1.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerFailing, corev1.ConditionFalse, "", ""),
	}

	for _, condition := range conditions {
		mcfgv1.SetControllerConfigStatusCondition(&controllerCfg.Status, *condition)
	}

	controllerCfg.Status.ObservedGeneration = 1

	_, err := clientSet.MachineconfigurationV1Interface.ControllerConfigs().UpdateStatus(ctx, controllerCfg, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// Simulates that the deployments managed by the operator have occurred and are available.
func handleDeployEvent(ctx context.Context, t *testing.T, event watch.Event, clientSet *framework.ClientSet) {
	if event.Type != watch.Added {
		return
	}

	deploy := event.Object.(*appsv1.Deployment)

	deploy.Status.ObservedGeneration = 2
	deploy.Status.UpdatedReplicas = 1
	deploy.Status.Replicas = 1
	deploy.Status.UnavailableReplicas = 0

	_, err := clientSet.AppsV1Interface.Deployments(deploy.Namespace).UpdateStatus(ctx, deploy, metav1.UpdateOptions{})

	require.NoError(t, err)
}

// Simulates the DaemonSet pods managed by the operator are running.
func handleDaemonsetEvent(ctx context.Context, t *testing.T, event watch.Event, clientSet *framework.ClientSet) {
	if event.Type != watch.Added {
		return
	}

	ds := event.Object.(*appsv1.DaemonSet)

	ds.Status.ObservedGeneration = 2
	ds.Status.CurrentNumberScheduled = 6
	ds.Status.DesiredNumberScheduled = 6
	ds.Status.UpdatedNumberScheduled = 6
	ds.Status.NumberReady = 6
	ds.Status.NumberUnavailable = 0

	_, err := clientSet.AppsV1Interface.DaemonSets(ds.Namespace).UpdateStatus(ctx, ds, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// Creates all of the K8s objects needed for the test to run.
func createKubeObjs(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) {
	t.Cleanup(createClusterVersion(ctx, t, clientSet))
	t.Cleanup(createClusterOperator(ctx, t, clientSet, "kube-apiserver"))
	t.Cleanup(createCluster(ctx, t, clientSet))
	t.Cleanup(createNetwork(ctx, t, clientSet))
	t.Cleanup(createMachineConfigPool(ctx, t, "master", clientSet))
	t.Cleanup(createMachineConfigPool(ctx, t, "worker", clientSet))

	objects := []runtime.Object{
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "root-ca",
				Namespace: "kube-system",
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			Data: map[string]string{
				// TODO: Use a dummy cert
				"ca.crt": "something",
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "initial-kube-apiserver-server-ca",
				Namespace: "openshift-config",
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			Data: map[string]string{
				// TODO: Use a dummy cert
				"ca-bundle.crt": "something",
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      osImageConfigMapName,
				Namespace: ctrlcommon.MCONamespace,
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			Data: map[string]string{
				"releaseVersion":       testReleaseVersion,
				"baseOSContainerImage": "something-else",
				"osImageURL":           "something-else",
			},
		},
	}

	tf := envtest.NewTestFixtures()

	objects = append(objects, tf.AsRuntimeObjects()...)
	envtest.CreateObjects(t, clientSet, objects...)

	nodeList, err := clientSet.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, node := range nodeList.Items {
		mutNode := node.DeepCopy()
		mutNode.Status.NodeInfo.KubeletVersion = testKubeletVersion

		_, err = clientSet.CoreV1Interface.Nodes().UpdateStatus(ctx, mutNode, metav1.UpdateOptions{})
		require.NoError(t, err)
	}

	// TODO: Wire up the cleanup function returned by
	// createMachineAPINamespace(). Currently, if we delete the namespace, the
	// objects contained therein (created by the operator under test) are not
	// deleted; presumably because their finalizers are not running in our test
	// environment. This becomes problematic when we try to clean the environment
	// between test cases. As a workaround, one could have separate
	// envtest.RunEnvTestCases() calls for each test case. However, this comes
	// with the additional time burden of the environment setup being performed
	// multiple times.
	createMachineAPINamespace(ctx, t, clientSet)
}

// Creates the openshift-machine-api namespace
func createMachineAPINamespace(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) func() {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-machine-api",
		},
	}

	_, err := clientSet.CoreV1Interface.Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	// TODO: Wire up the cleanup func so that it runs so that we don't have to perform this check.
	// The cleanup func
	if err != nil && !k8sErrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	return func() {
		err := clientSet.CoreV1Interface.Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}

// Creates machine configs with the appropriate annotations and status
func createMachineConfig(ctx context.Context, t *testing.T, name, nodeRoleLabel string, clientSet *framework.ClientSet) func() {
	filename := "/etc/" + name
	contents := "contents for " + filename
	mcfg := helpers.NewMachineConfig(name, map[string]string{nodeRoleLabel: ""}, "", []ign3types.File{
		helpers.CreateEncodedIgn3File(filename, contents, 0755),
	})

	mcfg.Annotations = map[string]string{
		// The actual version is injected at bvild-time, so this placeholder will at least let us get past here.
		ctrlcommon.GeneratedByControllerVersionAnnotationKey: "was-not-built-properly",
		ctrlcommon.ReleaseImageVersionAnnotationKey:          testReleaseVersion,
	}

	mcfg.Spec.OSImageURL = "something-else"

	_, err := clientSet.MachineconfigurationV1Interface.MachineConfigs().Create(ctx, mcfg, metav1.CreateOptions{})
	require.NoError(t, err)

	return func() {
		err := clientSet.MachineconfigurationV1Interface.MachineConfigs().Delete(ctx, mcfg.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}

// Creates a cluster object
func createCluster(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) func() {
	c := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
			InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
		},
	}

	i, err := clientSet.Infrastructures().Create(ctx, c, metav1.CreateOptions{})
	require.NoError(t, err)

	i.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
	i.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode

	_, err = clientSet.Infrastructures().UpdateStatus(ctx, i, metav1.UpdateOptions{})
	require.NoError(t, err)

	return func() {
		err := clientSet.Infrastructures().Delete(ctx, "cluster", metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}

// Creates a network object
func createNetwork(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) func() {
	n := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.NetworkSpec{
			ServiceNetwork: []string{
				"10.0.0.0/24",
			},
		},
	}

	_, err := clientSet.Networks().Create(ctx, n, metav1.CreateOptions{})
	require.NoError(t, err)

	return func() {
		err := clientSet.Networks().Delete(ctx, n.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}

// createClusterVersion creates a ClusterVersion with the correct status to allow the
// container runtime config controller to create the registry configuration
func createClusterVersion(ctx context.Context, t *testing.T, clientSet *framework.ClientSet) func() {
	cv := &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
	}
	cv, err := clientSet.ClusterVersions().Create(ctx, cv, metav1.CreateOptions{})
	require.NoError(t, err)

	cv.Status.Desired.Image = "release-image"
	cv, err = clientSet.ClusterVersions().UpdateStatus(ctx, cv, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, cv.Status.Desired.Image)

	return func() {
		err := clientSet.ClusterVersions().Delete(ctx, cv.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}

// Creats a cluster operator object
func createClusterOperator(ctx context.Context, t *testing.T, clientSet *framework.ClientSet, name string) func() {
	co := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: configv1.ClusterOperatorSpec{},
	}

	co, err := clientSet.ConfigV1Interface.ClusterOperators().Create(ctx, co, metav1.CreateOptions{})
	require.NoError(t, err)

	operandVersion := []configv1.OperandVersion{
		{
			Name:    name,
			Version: testKubeletVersion,
		},
	}

	co.Status.Versions = operandVersion
	co, err = clientSet.ConfigV1Interface.ClusterOperators().UpdateStatus(ctx, co, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Equal(t, co.Status.Versions, operandVersion)

	return func() {
		err := clientSet.ConfigV1Interface.ClusterOperators().Delete(ctx, co.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}

// Creates a MachineConfigPool object
func createMachineConfigPool(ctx context.Context, t *testing.T, name string, clientSet *framework.ClientSet) func() {
	configName := "control-plane-config"
	nodeRole := envtest.ControlPlaneNodeRole

	if name == "worker" {
		configName = "worker-config"
		nodeRole = envtest.WorkerNodeRole
	}

	mcCleanupFunc := createMachineConfig(ctx, t, configName, nodeRole, clientSet)

	selector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, nodeRole, "")

	mcp := helpers.NewMachineConfigPool(name, nil, selector, "v0")

	mcp, err := clientSet.MachineconfigurationV1Interface.MachineConfigPools().Create(ctx, mcp, metav1.CreateOptions{})
	require.NoError(t, err)

	conditions := []*mcfgv1.MachineConfigPoolCondition{
		mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionFalse, "", ""),
		mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded, corev1.ConditionFalse, "", ""),
		mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionFalse, "", ""),
		mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionFalse, "", ""),
		mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdated, corev1.ConditionTrue, "", ""),
	}

	for _, condition := range conditions {
		mcfgv1.SetMachineConfigPoolCondition(&mcp.Status, *condition)
	}

	configStatus := mcfgv1.MachineConfigPoolStatusConfiguration{
		ObjectReference: corev1.ObjectReference{
			Name: configName,
		},
		Source: []corev1.ObjectReference{
			{
				APIVersion: mcfgv1.GroupVersion.String(),
				Kind:       "MachineConfig",
				Name:       configName,
			},
		},
	}

	mcp.Spec.Configuration = configStatus
	mcp.Status.Configuration = configStatus
	mcp.Status.MachineCount = 3
	mcp.Status.ObservedGeneration = 2

	_, err = clientSet.MachineconfigurationV1Interface.MachineConfigPools().UpdateStatus(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	return func() {
		mcCleanupFunc()

		err := clientSet.MachineconfigurationV1Interface.MachineConfigPools().Delete(ctx, mcp.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
}
