package kubeletconfig

import (
	"bytes"
	"fmt"
	"reflect"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeletconfigscheme "k8s.io/kubernetes/pkg/kubelet/apis/config/scheme"
)

func createNewKubeletIgnition(jsonConfig []byte) igntypes.Config {
	mode := 0644
	du := dataurl.New(jsonConfig, "text/plain")
	du.Encoding = dataurl.EncodingASCII
	tempFile := igntypes.File{
		Node: igntypes.Node{
			Filesystem: "root",
			Path:       "/etc/kubernetes/kubelet.conf",
		},
		FileEmbedded1: igntypes.FileEmbedded1{
			Mode: &mode,
			Contents: igntypes.FileContents{
				Source: du.String(),
			},
		},
	}
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, tempFile)
	return tempIgnConfig
}

func createNewDefaultFeatureGate() *osev1.FeatureGate {
	return &osev1.FeatureGate{
		Spec: osev1.FeatureGateSpec{
			FeatureSet: osev1.Default,
		},
	}
}

func findKubeletConfig(mc *mcfgv1.MachineConfig) (*igntypes.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == "/etc/kubernetes/kubelet.conf" {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("Could not find Kubelet Config")
}

func getManagedFeaturesKey(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("98-%s-%s-kubelet", pool.Name, pool.ObjectMeta.UID)
}

func getManagedKubeletConfigKey(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("99-%s-%s-kubelet", pool.Name, pool.ObjectMeta.UID)
}

// validates a KubeletConfig and returns an error if invalid
func validateUserKubeletConfig(cfg *mcfgv1.KubeletConfig) error {
	if cfg.Spec.KubeletConfig == nil {
		return nil
	}
	kcValues := reflect.ValueOf(*cfg.Spec.KubeletConfig)
	if !kcValues.IsValid() {
		return fmt.Errorf("KubeletConfig is not valid")
	}
	for _, bannedFieldName := range blacklistKubeletConfigurationFields {
		v := kcValues.FieldByName(bannedFieldName)
		if !v.IsValid() {
			continue
		}
		err := fmt.Errorf("%v is not allowed to be set", bannedFieldName)
		switch v.Kind() {
		case reflect.Slice:
			if v.Len() > 0 {
				return err
			}
		case reflect.String:
			if v.String() != "" {
				return err
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if v.Int() != 0 {
				return err
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if v.Uint() != 0 {
				return err
			}
		case reflect.Struct:
			if v.Type().String() == "v1.Duration" {
				d := v.Interface().(metav1.Duration)
				if d.Duration.String() != "0s" {
					return err
				}
			}
		case reflect.Map:
			if len(v.MapKeys()) > 0 {
				return err
			}
		default:
			return fmt.Errorf("Invalid type in field %v", bannedFieldName)
		}
	}

	return nil
}

func wrapErrorWithCondition(err error, args ...interface{}) mcfgv1.KubeletConfigCondition {
	var condition *mcfgv1.KubeletConfigCondition
	if err != nil {
		condition = mcfgv1.NewKubeletConfigCondition(
			mcfgv1.KubeletConfigFailure,
			corev1.ConditionFalse,
			fmt.Sprintf("Error: %v", err),
		)
	} else {
		condition = mcfgv1.NewKubeletConfigCondition(
			mcfgv1.KubeletConfigSuccess,
			corev1.ConditionTrue,
			"Success",
		)
	}
	if len(args) > 0 {
		format, ok := args[0].(string)
		if ok {
			condition.Message = fmt.Sprintf(format, args[:1]...)
		}
	}
	return *condition
}

func decodeKubeletConfig(data []byte) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	config := &kubeletconfigv1beta1.KubeletConfiguration{}
	d := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), len(data))
	if err := d.Decode(config); err != nil {
		return nil, err
	}
	return config, nil
}

func encodeKubeletConfig(internal *kubeletconfigv1beta1.KubeletConfiguration, targetVersion schema.GroupVersion) ([]byte, error) {
	encoder, err := newKubeletconfigJSONEncoder(targetVersion)
	if err != nil {
		return nil, err
	}
	data, err := runtime.Encode(encoder, internal)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func newKubeletconfigJSONEncoder(targetVersion schema.GroupVersion) (runtime.Encoder, error) {
	_, codecs, err := kubeletconfigscheme.NewSchemeAndCodecs()
	if err != nil {
		return nil, err
	}
	mediaType := "application/json"
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), mediaType)
	if !ok {
		return nil, fmt.Errorf("unsupported media type %q", mediaType)
	}
	return codecs.EncoderForVersion(info.Serializer, targetVersion), nil
}
