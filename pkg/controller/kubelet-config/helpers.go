package kubeletconfig

import (
	"bytes"
	"encoding/json"
	"fmt"

	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
	osev1 "github.com/openshift/api/config/v1"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

func createNewKubeletLogLevelIgnition(level int32) *ign3types.File {
	config := fmt.Sprintf("[Service]\nEnvironment=\"KUBELET_LOG_LEVEL=%d\"\n", level)

	mode := 0644
	overwrite := true
	du := dataurl.New([]byte(config), "text/plain")
	du.Encoding = dataurl.EncodingASCII
	duStr := du.String()

	return &ign3types.File{
		Node: ign3types.Node{
			Path:      "/etc/systemd/system/kubelet.service.d/20-logging.conf",
			Overwrite: &overwrite,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Mode: &mode,
			Contents: ign3types.Resource{
				Source: &(duStr),
			},
		},
	}
}

func createNewKubeletIgnition(jsonConfig []byte) *ign3types.File {
	// Want the kubelet.conf file to have the pretty JSON formatting
	buf := new(bytes.Buffer)
	json.Indent(buf, jsonConfig, "", "  ")

	mode := 0644
	overwrite := true
	du := dataurl.New(buf.Bytes(), "text/plain")
	du.Encoding = dataurl.EncodingASCII
	duStr := du.String()

	return &ign3types.File{
		Node: ign3types.Node{
			Path:      "/etc/kubernetes/kubelet.conf",
			Overwrite: &overwrite,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Mode: &mode,
			Contents: ign3types.Resource{
				Source: &(duStr),
			},
		},
	}
}

func createNewDefaultFeatureGate() *osev1.FeatureGate {
	return &osev1.FeatureGate{
		Spec: osev1.FeatureGateSpec{
			FeatureGateSelection: osev1.FeatureGateSelection{
				FeatureSet: osev1.Default,
			},
		},
	}
}

func findKubeletConfig(mc *mcfgv1.MachineConfig) (*ign3types.File, error) {
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Kubelet Ignition config failed with error: %v", err)
	}
	for _, c := range ignCfg.Storage.Files {
		if c.Path == "/etc/kubernetes/kubelet.conf" {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("Could not find Kubelet Config")
}

func getManagedKubeletConfigKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface) (string, error) {
	return ctrlcommon.GetManagedKey(pool, client, "99", "kubelet", getManagedKubeletConfigKeyDeprecated(pool))
}

func getManagedFeaturesKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface) (string, error) {
	return ctrlcommon.GetManagedKey(pool, client, "98", "kubelet", getManagedFeaturesKeyDeprecated(pool))
}

// Deprecated: use getManagedFeaturesKey
func getManagedFeaturesKeyDeprecated(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("98-%s-%s-kubelet", pool.Name, pool.ObjectMeta.UID)
}

// Deprecated: use getManagedKubeletConfigKey
func getManagedKubeletConfigKeyDeprecated(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("99-%s-%s-kubelet", pool.Name, pool.ObjectMeta.UID)
}

// validates a KubeletConfig and returns an error if invalid
// nolint:gocyclo
func validateUserKubeletConfig(cfg *mcfgv1.KubeletConfig) error {
	if cfg.Spec.LogLevel != nil && (*cfg.Spec.LogLevel < 1 || *cfg.Spec.LogLevel > 10) {
		return fmt.Errorf("KubeletConfig's LogLevel is not valid [1,10]: %v", cfg.Spec.LogLevel)
	}
	if cfg.Spec.KubeletConfig == nil || cfg.Spec.KubeletConfig.Raw == nil {
		return nil
	}
	kcDecoded, err := decodeKubeletConfig(cfg.Spec.KubeletConfig.Raw)
	if err != nil {
		return fmt.Errorf("KubeletConfig could not be unmarshalled, err: %v", err)
	}

	// Check all the fields a user cannot set within the KubeletConfig CR.
	// If a user were to set these values, the system may become unrecoverable
	// (ie: not recover after a reboot).
	// Therefore, if the KubeletConfig CR instance contains a non-zero or non-empty value
	// for one of the following fields, the MCC will not apply the CR and error out instead.
	if kcDecoded.CgroupDriver != "" {
		return fmt.Errorf("KubeletConfiguration: cgroupDriver is not allowed to be set, but contains: %s", kcDecoded.CgroupDriver)
	}
	if len(kcDecoded.ClusterDNS) > 0 {
		return fmt.Errorf("KubeletConfiguration: clusterDNS is not allowed to be set, but contains: %s", kcDecoded.ClusterDNS)
	}
	if kcDecoded.ClusterDomain != "" {
		return fmt.Errorf("KubeletConfiguration: clusterDomain is not allowed to be set, but contains: %s", kcDecoded.ClusterDomain)
	}
	if len(kcDecoded.FeatureGates) > 0 {
		return fmt.Errorf("KubeletConfiguration: featureGates is not allowed to be set, but contains: %v", kcDecoded.FeatureGates)
	}
	if kcDecoded.RuntimeRequestTimeout.Duration != 0 {
		return fmt.Errorf("KubeletConfiguration: runtimeRequestTimeout is not allowed to be set, but contains: %s", kcDecoded.RuntimeRequestTimeout.Duration)
	}
	if kcDecoded.StaticPodPath != "" {
		return fmt.Errorf("KubeletConfiguration: staticPodPath is not allowed to be set, but contains: %s", kcDecoded.StaticPodPath)
	}

	if kcDecoded.EvictionSoft != nil && len(kcDecoded.EvictionSoft) > 0 {
		if kcDecoded.EvictionSoftGracePeriod == nil || len(kcDecoded.EvictionSoftGracePeriod) == 0 {
			return fmt.Errorf("KubeletConfiguration: EvictionSoftGracePeriod must be set when evictionSoft is defined, evictionSoft: %v", kcDecoded.EvictionSoft)
		}

		for k := range kcDecoded.EvictionSoft {
			if _, ok := kcDecoded.EvictionSoftGracePeriod[k]; !ok {
				return fmt.Errorf("KubeletConfiguration: evictionSoft[%s] is defined but EvictionSoftGracePeriod[%s] is not set", k, k)
			}
		}
	}

	reservedResources := []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory, v1.ResourceEphemeralStorage}

	if kcDecoded.KubeReserved != nil && len(kcDecoded.KubeReserved) > 0 {
		for _, rr := range reservedResources {
			if val, ok := kcDecoded.KubeReserved[rr.String()]; ok {
				q, err := resource.ParseQuantity(val)
				if err != nil {
					return fmt.Errorf("KubeletConfiguration: invalid value specified for %s reservation in kubeReserved, %s", rr.String(), val)
				}
				if q.Sign() == -1 {
					return fmt.Errorf("KubeletConfiguration: %s reservation value cannot be negative in kubeReserved", rr.String())
				}
			}
		}
	}

	if kcDecoded.SystemReserved != nil && len(kcDecoded.SystemReserved) > 0 {
		for _, rr := range reservedResources {
			if val, ok := kcDecoded.SystemReserved[rr.String()]; ok {
				q, err := resource.ParseQuantity(val)
				if err != nil {
					return fmt.Errorf("KubeletConfiguration: invalid value specified for %s reservation in systemReserved, %s", rr.String(), val)
				}
				if q.Sign() == -1 {
					return fmt.Errorf("KubeletConfiguration: %s reservation value cannot be negative in systemReserved", rr.String())
				}
			}
		}
	}

	if kcDecoded.EvictionHard != nil && len(kcDecoded.EvictionHard) > 0 {
		for _, rr := range reservedResources {
			if val, ok := kcDecoded.EvictionHard[rr.String()]; ok {
				q, err := resource.ParseQuantity(val)
				if err != nil {
					return fmt.Errorf("KubeletConfiguration: invalid value specified for %s reservation in evictionHard, %s", rr.String(), val)
				}
				if q.Sign() == -1 {
					return fmt.Errorf("KubeletConfiguration: %s eviction value cannot be negative in evictionHard", rr.String())
				}
			}
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
	scheme := runtime.NewScheme()
	kubeletconfigv1beta1.AddToScheme(scheme)
	codecs := serializer.NewCodecFactory(scheme)
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), runtime.ContentTypeJSON)
	if !ok {
		return nil, fmt.Errorf("unsupported media type %q", runtime.ContentTypeJSON)
	}
	return codecs.EncoderForVersion(info.Serializer, targetVersion), nil
}
