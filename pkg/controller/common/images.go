package common

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clientset "k8s.io/client-go/kubernetes"
)

const (
	imagesConfigMapImagesField                 = "images.json"
	osImageUrlConfigMapStreamsField            = "streams.json"
	osImageUrlConfigMapStreamsDefaultImagesTag = "rhel-coreos"
)

// Images contain data derived from what github.com/openshift/installer's
// bootkube.sh provides.  If you want to add a new image, you need
// to "ratchet" the change as follows:
//
// Add the image here and also a CLI option with a default value
// Change the installer to pass that arg with the image from the CVO
// (some time later) Change the option to required and drop the default
type Images struct {
	ReleaseVersion string `json:"releaseVersion,omitempty"`
	RenderConfigImages
	ControllerConfigImages
}

// RenderConfigImages are image names used to render templates under ./manifests/
type RenderConfigImages struct {
	MachineConfigOperator string `json:"machineConfigOperator"`
	// These have to be named differently from the ones in ControllerConfigImages
	// or we get errors about ambiguous selectors because both structs are
	// combined in the Images struct.
	KeepalivedBootstrap          string `json:"keepalived"`
	CorednsBootstrap             string `json:"coredns"`
	BaremetalRuntimeCfgBootstrap string `json:"baremetalRuntimeCfg"`
	OauthProxy                   string `json:"oauthProxy"`
	KubeRbacProxy                string `json:"kubeRbacProxy"`
}

// ControllerConfigImages are image names used to render templates under ./templates/
type ControllerConfigImages struct {
	InfraImage          string `json:"infraImage"`
	Keepalived          string `json:"keepalivedImage"`
	Coredns             string `json:"corednsImage"`
	Haproxy             string `json:"haproxyImage"`
	BaremetalRuntimeCfg string `json:"baremetalRuntimeCfgImage"`
}

type OSImageURLStreamConfig struct {
	BaseOSContainerImage           string `json:"baseOSContainerImage"`
	BaseOSExtensionsContainerImage string `json:"baseOSExtensionsContainerImage"`
}
type OSImageURLStreamsConfig struct {
	Default string                            `json:"default"`
	Streams map[string]OSImageURLStreamConfig `json:"streams"`
}

func (o *OSImageURLStreamsConfig) GetOSImageURLsForStream(stream string) OSImageURLStreamConfig {
	tagetStream, exists := o.Streams[stream]
	if !exists {
		return o.GetOSImageURLsForDefaultStream()
	}
	return tagetStream
}

func (o *OSImageURLStreamsConfig) GetOSImageURLsForDefaultStream() OSImageURLStreamConfig {
	return o.Streams[o.Default]
}

func (o *OSImageURLStreamsConfig) OSImageURLStreamExists(stream string) bool {
	_, exists := o.Streams[stream]
	return exists
}

func NewOSImageURLStreamsConfigFromBytes(buf []byte) (*OSImageURLStreamsConfig, error) {
	streamsConfig := &OSImageURLStreamsConfig{}
	if err := json.Unmarshal(buf, &streamsConfig); err != nil {
		return nil, fmt.Errorf("could not parse streams config bytes: %w", err)
	}
	if streamsConfig.Default == "" {
		return nil, fmt.Errorf("invalid osimagerul ConfigMap. The default stream cannot be empty")
	}

	if _, ok := streamsConfig.Streams[streamsConfig.Default]; !ok {
		return nil, fmt.Errorf(
			"invalid osimagerul ConfigMap. The default stream %s is not part of the declared streams %s",
			streamsConfig.Default,
			strings.Join(slices.Collect(maps.Keys(streamsConfig.Streams)), ", "),
		)
	}
	return streamsConfig, nil
}

func NewOSImageURLStreamsConfigDefault(osContainerImage, osContainerExtensionImage string) *OSImageURLStreamsConfig {
	return &OSImageURLStreamsConfig{
		Default: osImageUrlConfigMapStreamsDefaultImagesTag,
		Streams: map[string]OSImageURLStreamConfig{
			osImageUrlConfigMapStreamsDefaultImagesTag: {
				BaseOSContainerImage:           osContainerImage,
				BaseOSExtensionsContainerImage: osContainerExtensionImage,
			},
		},
	}
}

type OSImageURLConfig struct {
	ReleaseVersion string
	StreamsConfig  OSImageURLStreamsConfig
}

func ParseImagesFromBytes(in []byte) (*Images, error) {
	img := &Images{}
	if err := json.Unmarshal(in, img); err != nil {
		return nil, fmt.Errorf("could not parse images.json bytes: %w", err)
	}
	return img, nil
}

// Reads the contents of the provided ConfigMap into an OSImageURLConfig struct.
func ParseOSImageURLConfigMap(cm *corev1.ConfigMap) (*OSImageURLConfig, error) {
	if err := validateMCOConfigMap(
		cm,
		MachineConfigOSImageURLConfigMapName,
		[]string{"baseOSContainerImage", "baseOSExtensionsContainerImage", "releaseVersion"},
	); err != nil {
		return nil, err
	}

	// For now handle the streams like they are optional and populate them with a sane default
	// in case they are not present
	streamsData, ok := cm.Data[osImageUrlConfigMapStreamsField]
	var streamsConfig *OSImageURLStreamsConfig
	if ok {
		var err error
		streamsConfig, err = NewOSImageURLStreamsConfigFromBytes([]byte(streamsData))
		if err != nil {
			return nil, err
		}
	} else {
		// The pre-streams fields that holds the cluster-wide OS images
		// Always pointing to the rhel-coreos (osImageUrlConfigMapStreamsDefaultImagesTag) tag
		legacyBaseOSContainerImage := cm.Data["baseOSContainerImage"]
		legacyBaseOSExtensionsContainerImage := cm.Data["baseOSExtensionsContainerImage"]
		streamsConfig = NewOSImageURLStreamsConfigDefault(legacyBaseOSContainerImage, legacyBaseOSExtensionsContainerImage)
	}

	return &OSImageURLConfig{
		ReleaseVersion: cm.Data["releaseVersion"],
		StreamsConfig:  *streamsConfig,
	}, nil
}

// Validates a given ConfigMap in the MCO namespace. Valid in this case means the following:
// 1. The name matches what was provided.
// 2. The namespace is set to the MCO's namespace.
// 3. The data field has all of the expected keys.
func validateMCOConfigMap(cm *corev1.ConfigMap, name string, reqDataKeys []string) error {
	if cm.Name != name {
		return fmt.Errorf("invalid ConfigMap, expected %s", name)
	}

	if cm.Namespace != MCONamespace {
		return fmt.Errorf("invalid namespace, expected %s", MCONamespace)
	}

	if reqDataKeys != nil {
		for _, reqKey := range reqDataKeys {
			if _, ok := cm.Data[reqKey]; !ok {
				return fmt.Errorf("expected missing data key %q to be present in ConfigMap %s", reqKey, cm.Name)
			}
		}
	}
	return nil
}

func parseJsonFromConfigMap[T any](cm *corev1.ConfigMap, name string, value *T) error {
	if err := validateMCOConfigMap(cm, MachineConfigOperatorImagesConfigMapName, []string{name}); err != nil {
		return err
	}

	if err := json.Unmarshal([]byte(cm.Data[name]), value); err != nil {
		return fmt.Errorf("could not parse %s bytes: %w", name, err)
	}
	return nil
}

// Gets and parses the OSImageURL data from the machine-config-osimageurl ConfigMap.
func GetOSImageURLConfig(ctx context.Context, kubeclient clientset.Interface) (*OSImageURLConfig, error) {
	cm, err := kubeclient.CoreV1().ConfigMaps(MCONamespace).Get(ctx, MachineConfigOSImageURLConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get ConfigMap %s: %w", MachineConfigOSImageURLConfigMapName, err)
	}

	return ParseOSImageURLConfigMap(cm)
}

// Gets and parse the Images data from the machine-config-operator-images ConfigMap.
func GetImagesConfig(ctx context.Context, kubeclient clientset.Interface) (*Images, error) {
	cm, err := kubeclient.CoreV1().ConfigMaps(MCONamespace).Get(ctx, MachineConfigOperatorImagesConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get configmap %s: %w", MachineConfigOperatorImagesConfigMapName, err)
	}

	images := &Images{}
	if err := parseJsonFromConfigMap(cm, imagesConfigMapImagesField, images); err != nil {
		return nil, fmt.Errorf("could not parse configmap %s: %w", cm.Name, err)
	}
	return images, nil
}
