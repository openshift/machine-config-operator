package kubeletconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/imdario/mergo"
	osev1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

const (
	emptyInput      = ""
	cgroupv2Feature = "CGroupsV2"
)

func createNewKubeletDynamicSystemReservedIgnition(autoSystemReserved *bool, userDefinedSystemReserved map[string]string) *ign3types.File {
	var autoNodeSizing string
	var systemReservedMemory string
	var systemReservedCPU string

	if autoSystemReserved == nil {
		autoNodeSizing = "false"
	} else {
		autoNodeSizing = strconv.FormatBool(*autoSystemReserved)
	}

	if val, ok := userDefinedSystemReserved["memory"]; ok {
		systemReservedMemory = val
	} else {
		systemReservedMemory = "1Gi"
	}

	if val, ok := userDefinedSystemReserved["cpu"]; ok {
		systemReservedCPU = val
	} else {
		systemReservedCPU = "500m"
	}

	config := fmt.Sprintf("NODE_SIZING_ENABLED=%s\nSYSTEM_RESERVED_MEMORY=%s\nSYSTEM_RESERVED_CPU=%s\n", autoNodeSizing, systemReservedMemory, systemReservedCPU)
	r := ctrlcommon.NewIgnFileBytesOverwriting("/etc/node-sizing-enabled.env", []byte(config))
	return &r
}

func createNewKubeletLogLevelIgnition(level int32) *ign3types.File {
	config := fmt.Sprintf("[Service]\nEnvironment=\"KUBELET_LOG_LEVEL=%d\"\n", level)
	r := ctrlcommon.NewIgnFileBytesOverwriting("/etc/systemd/system/kubelet.service.d/20-logging.conf", []byte(config))
	return &r
}

func createNewKubeletIgnition(jsonConfig []byte) *ign3types.File {
	// Want the kubelet.conf file to have the pretty JSON formatting
	buf := new(bytes.Buffer)
	json.Indent(buf, jsonConfig, "", "  ")

	r := ctrlcommon.NewIgnFileBytesOverwriting("/etc/kubernetes/kubelet.conf", buf.Bytes())
	return &r
}

func createNewDefaultFeatureGate() *osev1.FeatureGate {
	return &osev1.FeatureGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterFeatureInstanceName,
		},
		Spec: osev1.FeatureGateSpec{
			FeatureGateSelection: osev1.FeatureGateSelection{
				FeatureSet: osev1.Default,
			},
		},
	}
}

func createNewDefaultNodeconfig() *osev1.Node {
	return &osev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterNodeInstanceName,
		},
		Spec: osev1.NodeSpec{},
	}
}

func getFeatures(ctrl *Controller) (*osev1.FeatureGate, error) {
	features, err := ctrl.featLister.Get(ctrlcommon.ClusterFeatureInstanceName)
	if errors.IsNotFound(err) {
		features = createNewDefaultFeatureGate()
	} else if err != nil {
		return nil, err
	}
	return features, nil
}

func getConfigNode(ctrl *Controller, key string) (*osev1.Node, error) {
	nodeConfig, err := ctrl.nodeConfigLister.Get(ctrlcommon.ClusterNodeInstanceName)
	if errors.IsNotFound(err) {
		return nil, fmt.Errorf("missing node configuration, key: %v", key)
	} else if err != nil {
		return nil, err
	}
	return nodeConfig, nil
}

// updateOriginalKubeConfigwithNodeConfig updates the original Kubelet Configuration based on the Nodespecific configuration
func updateOriginalKubeConfigwithNodeConfig(node *osev1.Node, originalKubeletConfig *kubeletconfigv1beta1.KubeletConfiguration) error {
	if node == nil {
		return fmt.Errorf("node configuration not found, failed to update the original kubelet configuration")
	}
	if reflect.DeepEqual(node.Spec, osev1.NodeSpec{}) {
		return fmt.Errorf("empty node resource spec found")
	}
	// updating the kubelet specific fields based on the Node's workerlatency profile.
	// (TODO): The durations can be replaced with the defined constants in the openshift/api repository once the respective changes are merged.
	switch node.Spec.WorkerLatencyProfile {
	case osev1.MediumUpdateAverageReaction:
		originalKubeletConfig.NodeStatusUpdateFrequency = metav1.Duration{Duration: osev1.MediumNodeStatusUpdateFrequency}
	case osev1.LowUpdateSlowReaction:
		originalKubeletConfig.NodeStatusUpdateFrequency = metav1.Duration{Duration: osev1.LowNodeStatusUpdateFrequency}
	case osev1.DefaultUpdateDefaultReaction:
		originalKubeletConfig.NodeStatusUpdateFrequency = metav1.Duration{Duration: osev1.DefaultNodeStatusUpdateFrequency}
	case emptyInput:
		return nil
	default:
		return fmt.Errorf("unknown worker latency profile type found %v, failed to update the original kubelet configuration", node.Spec.WorkerLatencyProfile)
	}
	// The kubelet configuration can be updated based on the cgroupmode as well here.
	return nil
}

// isTechPreviewNoUpgradeEnabled returns a boolean accordingly if a feature is enabled
// (TODO) This can be generically used in future by passing any feature as an argument.
func isTechPreviewNoUpgradeEnabled(features *osev1.FeatureGate) bool {
	if features == nil {
		return false
	}
	featureSet, ok := osev1.FeatureSets[osev1.TechPreviewNoUpgrade]
	if !ok {
		return false
	}
	for _, val := range featureSet.Enabled {
		if val == cgroupv2Feature {
			return true
		}
	}
	return false
}

// updateMachineConfigwithCgroup updates the Machine Config object based on the cgroup mode present in the Config Node resource.
func updateMachineConfigwithCgroup(node *osev1.Node, mc *mcfgv1.MachineConfig) error {
	if node == nil {
		return fmt.Errorf("node configuration not found, failed to update the machine config with the cgroup information")
	}
	if reflect.DeepEqual(node.Spec, osev1.NodeSpec{}) {
		return fmt.Errorf("empty config node resource spec found")
	}
	if mc == nil || reflect.DeepEqual(mc.Spec, mcfgv1.MachineConfigSpec{}) {
		return fmt.Errorf("machine config not found, failed to update the machine config with the cgroup information")
	}
	// updating the Machine Config resource with the relevant cgroup config
	var (
		kernelArgsv1       = []string{"systemd.unified_cgroup_hierarchy=0", "systemd.legacy_systemd_cgroup_controller=1"}
		kernelArgsv2       = []string{"systemd.unified_cgroup_hierarchy=1", "cgroup_no_v1=\"all\"", "psi=1"}
		kernelArgstoRemove []string
		kernelArgstoAdd    []string
	)
	switch node.Spec.CgroupMode {
	case osev1.CgroupModeV1:
		kernelArgstoAdd = append(kernelArgstoAdd, kernelArgsv1...)
		kernelArgstoRemove = append(kernelArgstoRemove, kernelArgsv2...)
	case osev1.CgroupModeV2:
		kernelArgstoAdd = append(kernelArgstoAdd, kernelArgsv2...)
		kernelArgstoRemove = append(kernelArgstoRemove, kernelArgsv1...)
	case emptyInput:
		return nil
	default:
		return fmt.Errorf("unknown cgroup mode found %v, failed to update the machine config resource", node.Spec.CgroupMode)
	}
	for _, arg := range kernelArgstoRemove {
		for key, value := range mc.Spec.KernelArguments {
			if arg == value {
				mc.Spec.KernelArguments = append(mc.Spec.KernelArguments[:key], mc.Spec.KernelArguments[key+1:]...)
			}
		}
	}
	for _, arg := range kernelArgstoAdd {
		var present bool
		for _, value := range mc.Spec.KernelArguments {
			if arg == value {
				present = true
				break
			}
		}
		if !present {
			mc.Spec.KernelArguments = append(mc.Spec.KernelArguments, arg)
		}
	}
	return nil
}

func findKubeletConfig(mc *mcfgv1.MachineConfig) (*ign3types.File, error) {
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Kubelet Ignition config failed with error: %w", err)
	}
	for _, c := range ignCfg.Storage.Files {
		if c.Path == "/etc/kubernetes/kubelet.conf" {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Kubelet Config")
}

// nolint: dupl
func getManagedKubeletConfigKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface, cfg *mcfgv1.KubeletConfig) (string, error) {
	// Get all the kubelet config CRs
	kcListAll, err := client.MachineconfigurationV1().KubeletConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("error listing kubelet configs: %w", err)
	}

	// If there is no kubelet config in the list, return the default MC name with no suffix
	if kcListAll == nil || len(kcListAll.Items) == 0 {
		return ctrlcommon.GetManagedKey(pool, client, "99", "kubelet", getManagedKubeletConfigKeyDeprecated(pool))
	}

	var kcList []mcfgv1.KubeletConfig
	for _, kc := range kcListAll.Items {
		selector, err := metav1.LabelSelectorAsSelector(kc.Spec.MachineConfigPoolSelector)
		if err != nil {
			return "", fmt.Errorf("invalid label selector: %w", err)
		}
		if selector.Empty() || !selector.Matches(labels.Set(pool.Labels)) {
			continue
		}
		kcList = append(kcList, kc)
	}

	for _, kc := range kcList {
		if kc.Name != cfg.Name {
			continue
		}
		val, ok := kc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		if !ok {
			break
		}
		// if an MC name suffix exists, append it to the default MC name and return that as this kubelet config exists and
		// we are probably doing an update action on it
		if val != "" {
			return fmt.Sprintf("99-%s-generated-kubelet-%s", pool.Name, val), nil
		}
		// if the suffix val is "", mc name should not suffixed the cfg to be updated is the first kubelet config has been created
		return ctrlcommon.GetManagedKey(pool, client, "99", "kubelet", getManagedKubeletConfigKeyDeprecated(pool))
	}

	// If we are here, this means that a new kubelet config was created, so we have to calculate the suffix value for its MC name
	// if the kubelet config is the only one in the list, mc name should not suffixed since cfg is the first kubelet config to be created
	if len(kcList) == 1 {
		return ctrlcommon.GetManagedKey(pool, client, "99", "kubelet", getManagedKubeletConfigKeyDeprecated(pool))
	}
	suffixNum := 0
	// Go through the list of kubelet config objects created and get the max suffix value currently created
	for _, item := range kcList {
		val, ok := item.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		if ok && val != "" {
			// Convert the suffix value to int so we can look through the list and grab the max suffix created so far
			intVal, err := strconv.Atoi(val)
			if err != nil {
				return "", fmt.Errorf("error converting %s to int: %w", val, err)
			}
			if intVal > suffixNum {
				suffixNum = intVal
			}
		}
	}
	// The max suffix value that we can go till with this logic is 9 - this means that a user can create up to 10 different kubelet config CRs.
	// However, if there is a kc-1 mapping to mc-1 and kc-2 mapping to mc-2 and the user deletes kc-1, it will delete mc-1 but
	// then if the user creates a kc-new it will map to mc-3. This is what we want as the latest kubelet config created should be higher in priority
	// so that those changes can be rolled out to the nodes. But users will have to be mindful of how many kubelet config CRs they create. Don't think
	// anyone should ever have the need to create 10 when they can simply update an existing kubelet config unless it is to apply to another pool.
	if suffixNum+1 > ctrlcommon.MaxMCNameSuffix {
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

func getManagedNodeConfigKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface) (string, error) {
	return ctrlcommon.GetManagedKey(pool, client, "97", "kubelet", fmt.Sprintf("97-%s-%s-kubelet", pool.Name, pool.ObjectMeta.UID))
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
		return fmt.Errorf("KubeletConfig could not be unmarshalled, err: %w", err)
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
	if kcDecoded.StaticPodPath != "" {
		return fmt.Errorf("KubeletConfiguration: staticPodPath is not allowed to be set, but contains: %s", kcDecoded.StaticPodPath)
	}
	if kcDecoded.SystemReserved != nil && len(kcDecoded.SystemReserved) > 0 &&
		cfg.Spec.AutoSizingReserved != nil && *cfg.Spec.AutoSizingReserved {
		return fmt.Errorf("KubeletConfiguration: autoSizingReserved and systemdReserved cannot be set together")
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

// kubeletConfigToIgnFile converts a KubeletConfiguration to an Ignition File
func kubeletConfigToIgnFile(cfg *kubeletconfigv1beta1.KubeletConfiguration) (*ign3types.File, error) {
	cfgJSON, err := EncodeKubeletConfig(cfg, kubeletconfigv1beta1.SchemeGroupVersion)
	if err != nil {
		return nil, fmt.Errorf("could not encode kubelet configuration: %w", err)
	}
	cfgIgn := createNewKubeletIgnition(cfgJSON)
	return cfgIgn, nil
}

// generateKubeletIgnFiles generates the Ignition files from the kubelet config
func generateKubeletIgnFiles(kubeletConfig *mcfgv1.KubeletConfig, originalKubeConfig *kubeletconfigv1beta1.KubeletConfiguration) (*ign3types.File, *ign3types.File, *ign3types.File, error) {
	var (
		kubeletIgnition            *ign3types.File
		logLevelIgnition           *ign3types.File
		autoSizingReservedIgnition *ign3types.File
	)
	userDefinedSystemReserved := make(map[string]string)

	if kubeletConfig.Spec.KubeletConfig != nil && kubeletConfig.Spec.KubeletConfig.Raw != nil {
		specKubeletConfig, err := decodeKubeletConfig(kubeletConfig.Spec.KubeletConfig.Raw)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("could not deserialize the new Kubelet config: %w", err)
		}

		if val, ok := specKubeletConfig.SystemReserved["memory"]; ok {
			userDefinedSystemReserved["memory"] = val
			delete(specKubeletConfig.SystemReserved, "memory")
		}

		if val, ok := specKubeletConfig.SystemReserved["cpu"]; ok {
			userDefinedSystemReserved["cpu"] = val
			delete(specKubeletConfig.SystemReserved, "cpu")
		}

		// FeatureGates must be set from the FeatureGate.
		// Remove them here to prevent the specKubeletConfig merge overwriting them.
		specKubeletConfig.FeatureGates = nil

		// Merge the Old and New
		err = mergo.Merge(originalKubeConfig, specKubeletConfig, mergo.WithOverride)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("could not merge original config and new config: %w", err)
		}
	}

	// Encode the new config into an Ignition File
	kubeletIgnition, err := kubeletConfigToIgnFile(originalKubeConfig)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("could not encode JSON: %w", err)
	}

	if kubeletConfig.Spec.LogLevel != nil {
		logLevelIgnition = createNewKubeletLogLevelIgnition(*kubeletConfig.Spec.LogLevel)
	}
	if kubeletConfig.Spec.AutoSizingReserved != nil && len(userDefinedSystemReserved) == 0 {
		autoSizingReservedIgnition = createNewKubeletDynamicSystemReservedIgnition(kubeletConfig.Spec.AutoSizingReserved, userDefinedSystemReserved)
	}
	if len(userDefinedSystemReserved) > 0 {
		autoSizingReservedIgnition = createNewKubeletDynamicSystemReservedIgnition(nil, userDefinedSystemReserved)
	}

	return kubeletIgnition, logLevelIgnition, autoSizingReservedIgnition, nil
}
