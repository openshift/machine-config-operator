package kubeletconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	osev1 "github.com/openshift/api/config/v1"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterFeatureInstanceName,
		},
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

// nolint: dupl
func getManagedKubeletConfigKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface, cfg *mcfgv1.KubeletConfig) (string, error) {
	// Get all the kubelet config CRs
	kcList, err := client.MachineconfigurationV1().KubeletConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("error listing kubelet configs: %v", err)
	}
	// If there is no kubelet config in the list, return the default MC name with no suffix
	if kcList == nil || len(kcList.Items) == 0 {
		return ctrlcommon.GetManagedKey(pool, client, "99", "kubelet", getManagedKubeletConfigKeyDeprecated(pool))
	}
	for _, kc := range kcList.Items {
		if kc.Name != cfg.Name {
			continue
		}
		val, ok := kc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		// If we find a matching kubelet config and it is the only one in the list, then return the default MC name with no suffix
		if !ok && len(kcList.Items) < 9 {
			return ctrlcommon.GetManagedKey(pool, client, "99", "kubelet", getManagedKubeletConfigKeyDeprecated(pool))
		}
		// Otherwise if an MC name suffix exists, append it to the default MC name and return that as this kubelet config exists and
		// we are probably doing an update action on it
		if val != "" {
			return fmt.Sprintf("99-%s-generated-kubelet-%s", pool.Name, val), nil
		}
	}

	// If we are here, this means that a new kubelet config was created, so we have to calculate the suffix value for its MC name
	suffixNum := 0
	// Get the suffix value of the second to last item in the kubelet config list. We use the second to last item because
	// the most recent kubelet config being created has already been added to the list and is the kubelet config object that we are
	// trying to figure out the suffix value for
	val, ok := kcList.Items[len(kcList.Items)-2].GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
	if ok {
		// Convert the suffix value to int so we can add 1 to it for the MC name for the latest kubelet config object
		suffixNum, err = strconv.Atoi(val)
		if err != nil {
			return "", fmt.Errorf("error converting %s to int: %v", val, err)
		}
	}
	// The max suffix value that we can go till with this logic is 9 - this means that a user can create up to 10 different kubelet config CRs.
	// However, if there is a kc-1 mapping to mc-1 and kc-2 mapping to mc-2 and the user deletes kc-1, it will delete mc-1 but
	// then if the user creates a kc-new it will map to mc-3. This is what we want as the latest kubelet config created should be higher in priority
	// so that those changes can be rolled out to the nodes. But users will have to be mindful of how many kubelet config CRs they create. Don't think
	// anyone should ever have the need to create 10 when they can simply update an existing kubelet config unless it is to apply to another pool.
	if suffixNum+1 > 2 {
		return "", fmt.Errorf("max number of supported kubelet config (10) has been reached. Please delete old kubelet configs before retrying")
	}
	// Return the default MC name with the suffixNum+1 value appended to it
	return fmt.Sprintf("99-%s-generated-kubelet-%s", pool.Name, strconv.Itoa(suffixNum+1)), nil
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
			condition.Message = fmt.Sprintf(format, args[1:]...)
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

func EncodeKubeletConfig(internal *kubeletconfigv1beta1.KubeletConfiguration, targetVersion schema.GroupVersion) ([]byte, error) {
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
