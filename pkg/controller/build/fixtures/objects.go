package fixtures

import (
	"fmt"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	BaseImagePullSecretName  string = "base-image-pull-secret"
	finalImagePushSecretName string = "final-image-push-secret"
	JobUID                   string = "bfc35cd0f874c9bfdc586e6ba39f1896"
)

// Provides consistently instantiated objects for use in a given test.
type ObjectsForTest struct {
	MachineConfigPool *mcfgv1.MachineConfigPool
	MachineConfigs    []*mcfgv1.MachineConfig
	MachineOSConfig   *mcfgv1.MachineOSConfig
	MachineOSBuild    *mcfgv1.MachineOSBuild
}

// Provides the builders to create consistently instantiated objects for use in
// a given test. These are intended to be mutated for a specific test case.
type ObjectBuildersForTest struct {
	MachineConfigPoolBuilder *testhelpers.MachineConfigPoolBuilder
	MachineOSBuildBuilder    *testhelpers.MachineOSBuildBuilder
	MachineOSConfigBuilder   *testhelpers.MachineOSConfigBuilder
}

func (o *ObjectBuildersForTest) ToObjectsForTest() ObjectsForTest {
	mcp := o.MachineConfigPoolBuilder.MachineConfigPool()

	return ObjectsForTest{
		MachineConfigPool: mcp,
		MachineConfigs:    newMachineConfigsFromPool(mcp),
		MachineOSConfig:   o.MachineOSConfigBuilder.MachineOSConfig(),
		MachineOSBuild:    o.MachineOSBuildBuilder.MachineOSBuild(),
	}
}

// Converts the MachineConfigPool and MachineConfigs into a []runtime.Object
// array, suitable for passing into mcfgclient when instantiated.
func (o *ObjectsForTest) ToRuntimeObjects() []runtime.Object {
	out := []runtime.Object{o.MachineConfigPool}

	for _, item := range o.MachineConfigs {
		out = append(out, item)
	}

	return out
}

// Constructs a MachineOSConfig, MachineOSBuild, MachineConfigPool, and
// MachineConfigs with all of the objects' default fields being populated.
func NewObjectsForTest(poolName string) ObjectsForTest {
	b := NewObjectBuildersForTest(poolName)
	return b.ToObjectsForTest()
}

func getChildConfigs(poolName string, num int) []string {
	childConfigNames := []string{}

	for i := 1; i <= num; i++ {
		childConfigNames = append(childConfigNames, fmt.Sprintf("%s-config-%d", poolName, i))
	}

	return childConfigNames
}

// Creates the pre-configured object builders for a given test. These are
// preconfigured with the bare minimum values for most tests to pass. Specific
// tests may need additional values set or may need to be set to different
// values. This is expected and those values should be set within the test, not
// here.
func NewObjectBuildersForTest(poolName string) ObjectBuildersForTest {
	renderedConfigName := fmt.Sprintf("rendered-%s-1", poolName)
	moscName := poolName
	mosbName := fmt.Sprintf("%s-afc35db0f874c9bfdc586e6ba39f1504", poolName)

	moscBuilder := testhelpers.NewMachineOSConfigBuilder(moscName).
		WithMachineConfigPool(poolName).
		WithRenderedImagePushSecret(finalImagePushSecretName).
		WithRenderedImagePushSpec("registry.hostname.com/org/repo:latest").
		WithContainerfile(mcfgv1.NoArch, "FROM configs AS final\n\nRUN echo 'hi' > /etc/hi")

	mcpBuilder := testhelpers.NewMachineConfigPoolBuilder(poolName).
		WithChildConfigs(getChildConfigs(poolName, 5)).
		WithMachineConfig(renderedConfigName)

	mosbBuilder := testhelpers.NewMachineOSBuildBuilder(mosbName).
		WithDesiredConfig(renderedConfigName).
		WithLabels(map[string]string{
			constants.TargetMachineConfigPoolLabelKey: poolName,
			constants.RenderedMachineConfigLabelKey:   renderedConfigName,
			constants.MachineOSConfigNameLabelKey:     moscName,
		}).
		WithAnnotations(map[string]string{
			constants.JobUIDAnnotationKey: JobUID,
		})

	return ObjectBuildersForTest{
		MachineConfigPoolBuilder: mcpBuilder,
		MachineOSConfigBuilder:   moscBuilder,
		MachineOSBuildBuilder:    mosbBuilder,
	}
}

// Constructs all of the default ConfigMaps, secrets, etc. that are assumed to be present.
func defaultKubeObjects() []runtime.Object {
	legacyPullSecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	pullSecret := `{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}}`

	return []runtime.Object{
		getImagesConfigMap(),
		getOSImageURLConfigMap(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      finalImagePushSecretName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string][]byte{
				corev1.DockerConfigKey: []byte(legacyPullSecret),
			},
			Type: corev1.SecretTypeDockercfg,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      BaseImagePullSecretName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(pullSecret),
			},
			Type: corev1.SecretTypeDockerConfigJson,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ctrlcommon.GlobalPullSecretCopyName,
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
	}
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

// Gets an example machine-config-osimageurl ConfigMap.
func getOSImageURLConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.MachineConfigOSImageURLConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"baseOSContainerImage":           BaseOSContainerImage,
			"baseOSExtensionsContainerImage": BaseOSExtensionsContainerImage,
			"osImageURL":                     OSImageURL,
			"releaseVersion":                 ReleaseVersion,
		},
	}
}

const (
	BaseOSContainerImage           string = "registry.hostname.com/org/repo@sha256 string = 220a60ecd4a3c32c282622a625a54db9ba0ff55b5ba9c29c7064a2bc358b6a3e"
	BaseOSExtensionsContainerImage string = "registry.hostname.com/org/repo@sha256 string = 5fb4ba1a651bae8057ec6b5cdafc93fa7e0b7d944d6f02a4b751de4e15464def"
	ReleaseVersion                 string = "release-version"
	OSImageURL                     string = "registry.hostname.com/org/repo@sha256 string = 5be476dce1f7c1fbaf41bf9c0097e1725d7d26b74ea93543989d1a2b76fef4a5"
)

// Gets the OSImageURL struct that the machine-config-osimageurl ConfigMap
// would be marshalled into.
func OSImageURLConfig() *ctrlcommon.OSImageURLConfig {
	return &ctrlcommon.OSImageURLConfig{
		BaseOSContainerImage:           BaseOSContainerImage,
		BaseOSExtensionsContainerImage: BaseOSExtensionsContainerImage,
		ReleaseVersion:                 ReleaseVersion,
		OSImageURL:                     OSImageURL,
	}
}

func GetExpectedFinalImagePullspecForMachineOSBuild(mosb *mcfgv1.MachineOSBuild) string {
	digest := getDigest(mosb.Name)
	return "registry.hostname.com/org/repo@" + digest
}
