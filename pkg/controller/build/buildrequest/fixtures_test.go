package buildrequest

import (
	"fmt"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

type addlObjects struct {
	kubeObjects []runtime.Object
	mcfgObjects []runtime.Object
}

func getClientsForTest(addlObjects addlObjects) (clientset.Interface, mcfgclientset.Interface) {
	lobj := newLayeredObjectsForTest("worker")

	mcfgObjects := append(addlObjects.mcfgObjects, lobj.toRuntimeObjects()...)
	mcfgObjects = append(mcfgObjects, &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "machine-config-controller",
		},
	})

	legacyPullSecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	pullSecret := `{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}}`

	kubeObjects := append(addlObjects.kubeObjects, []runtime.Object{
		getImagesConfigMap(),
		getOSImageURLConfigMap(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "final-image-push-secret",
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string][]byte{
				corev1.DockerConfigKey: []byte(legacyPullSecret),
			},
			Type: corev1.SecretTypeDockercfg,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "base-image-pull-secret",
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(pullSecret),
			},
			Type: corev1.SecretTypeDockerConfigJson,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "current-image-pull-secret",
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(pullSecret),
			},
			Type: corev1.SecretTypeDockerConfigJson,
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "machine-config-operator",
				Namespace: ctrlcommon.MCONamespace,
			},
		},
	}...)

	return fakecorev1client.NewSimpleClientset(kubeObjects...), fakeclientmachineconfigv1.NewSimpleClientset(mcfgObjects...)
}

// Generates MachineConfigs from the given MachineConfigPool for insertion.
func newMachineConfigsFromPool(mcp *mcfgv1.MachineConfigPool) []*mcfgv1.MachineConfig {
	files := []ign3types.File{}

	out := []*mcfgv1.MachineConfig{}

	// Create individual MachineConfigs to accompany the child MachineConfigs referred to by our MachineConfigPool.
	for _, childConfig := range mcp.Spec.Configuration.Source {
		if childConfig.Kind != "MachineConfig" {
			continue
		}

		filename := fmt.Sprintf("/etc/%s", childConfig.Name)
		file := ctrlcommon.NewIgnFile(filename, childConfig.Name)
		files = append(files, file)

		out = append(out, testhelpers.NewMachineConfig(
			childConfig.Name,
			map[string]string{
				"machineconfiguration.openshift.io/role": mcp.Name,
			},
			"",
			[]ign3types.File{file}))
	}

	// Create a rendered MachineConfig to accompany our MachineConfigPool.
	out = append(out, testhelpers.NewMachineConfig(
		mcp.Spec.Configuration.Name,
		map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: "version-number",
			"machineconfiguration.openshift.io/role":             mcp.Name,
		},
		"",
		files))

	return out
}

// Gets an example machine-config-osimageurl ConfigMap.
func getOSImageURLConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.MachineConfigOSImageURLConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"baseOSContainerImage":           "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:12e89d631c0ca1700262583acfb856b6e7dbe94800cb38035d68ee5cc912411c",
			"baseOSExtensionsContainerImage": "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:5b6d901069e640fc53d2e971fa1f4802bf9dea1a4ffba67b8a17eaa7d8dfa336",
			"osImageURL":                     "",
			"releaseVersion":                 "4.14.0-0.ci-2023-05-29-125629",
		},
	}
}

// Gets an example machine-config-operator-images ConfigMap.
func getImagesConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.MachineConfigOperatorImagesConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"images.json": `{"machineConfigOperator": "mco.image.pullspec"}`,
		},
	}
}

type layeredObjectsForTest struct {
	mcp  *mcfgv1.MachineConfigPool
	mcs  []*mcfgv1.MachineConfig
	mosc *mcfgv1alpha1.MachineOSConfig
	mosb *mcfgv1alpha1.MachineOSBuild
}

func (l *layeredObjectsForTest) toRuntimeObjects() []runtime.Object {
	out := []runtime.Object{l.mcp}

	for _, item := range l.mcs {
		out = append(out, item)
	}

	return out
}

func newLayeredObjectsForTest(poolName string) layeredObjectsForTest {
	renderedConfigName := fmt.Sprintf("rendered-%s-1", poolName)
	layeredBuilder := testhelpers.NewLayeredBuilder(poolName).
		WithDesiredConfig(renderedConfigName)

	layeredBuilder.MachineOSConfigBuilder().
		WithMachineConfigPool(poolName).
		WithBaseImagePullSecret("base-image-pull-secret").
		WithRenderedImagePushSecret("final-image-push-secret").
		WithCurrentImagePullSecret("current-image-pull-secret").
		WithRenderedImagePushspec("registry.hostname.com/org/repo:latest").
		WithCurrentImagePullspec(fmt.Sprintf("registry.hostname.com/org/repo:%s", renderedConfigName))

	childConfigNames := []string{}
	for i := 1; i <= 5; i++ {
		childConfigNames = append(childConfigNames, fmt.Sprintf("%s-config-%d", poolName, i))
	}

	nodeRoleLabel := fmt.Sprintf("node-role.kubernetes.io/%s", poolName)
	nodeSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, nodeRoleLabel, "")

	layeredBuilder.MachineConfigPoolBuilder().
		WithChildConfigs(childConfigNames).
		WithNodeSelector(nodeSelector)

	mcp := layeredBuilder.MachineConfigPool()
	mosc := layeredBuilder.MachineOSConfig()
	mosb := layeredBuilder.MachineOSBuild()

	return layeredObjectsForTest{
		mcp:  mcp,
		mosc: mosc,
		mosb: mosb,
		mcs:  newMachineConfigsFromPool(mcp),
	}
}
