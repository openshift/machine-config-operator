package common

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"path/filepath"
	"strings"
	"text/template"

	"github.com/clarketm/json"
	kerr "k8s.io/apimachinery/pkg/api/errors"

	ign3 "github.com/coreos/ignition/v2/config/v3_4"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/ghodss/yaml"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
	k8sapiflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	"github.com/openshift/library-go/pkg/crypto"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"

	v1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

// strToPtr converts the input string to a pointer to itself
func strToPtr(s string) *string {
	return &s
}

// bootToPtr converts the input boolean to a pointer to itself
func boolToPtr(b bool) *bool {
	return &b
}

// MergeMachineConfigs combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ignition config from first object as base and appends all the rest.
// Kernel arguments are concatenated.
// It defaults to the OSImageURL provided by the CVO but allows a MC provided OSImageURL to take precedence.
func MergeMachineConfigs(configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig) (*mcfgv1.MachineConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	// Overall the sort is alphanumerical, but custom pool configuration should take priority.
	// Generally speaking if a custom pool is created, the expectation is that custom pool configuration should override base
	// worker configuration.
	// This mostly aims to help with generated configs (e.g. kubelet or containerruntime configs) where the pool name is
	// part of the MachineConfig name, which cannot be directly modified.
	var workerConfigs, otherConfigs []*mcfgv1.MachineConfig
	for _, config := range configs {
		if config.ObjectMeta.Labels == nil {
			// This shouldn't really be possible
			return nil, fmt.Errorf("Cannot find label in MachineConfig %s", config.ObjectMeta.Name)
		}
		if config.ObjectMeta.Labels[commonconsts.MachineConfigRoleLabel] == commonconsts.MachineConfigPoolWorker {
			workerConfigs = append(workerConfigs, config)
		} else {
			otherConfigs = append(otherConfigs, config)
		}
	}
	sort.SliceStable(workerConfigs, func(i, j int) bool { return workerConfigs[i].Name < workerConfigs[j].Name })
	sort.SliceStable(otherConfigs, func(i, j int) bool { return otherConfigs[i].Name < otherConfigs[j].Name })
	configs = append(configs[:0], append(workerConfigs, otherConfigs...)...)

	var fips bool
	var kernelType string
	var outIgn ign3types.Config
	var err error

	if configs[0].Spec.Config.Raw == nil {
		outIgn = ign3types.Config{
			Ignition: ign3types.Ignition{
				Version: ign3types.MaxVersion.String(),
			},
		}
	} else {
		outIgn, err = ParseAndConvertConfig(configs[0].Spec.Config.Raw)
		if err != nil {
			return nil, err
		}
	}

	for idx := 1; idx < len(configs); idx++ {
		if configs[idx].Spec.Config.Raw != nil {
			mergedIgn, err := ParseAndConvertConfig(configs[idx].Spec.Config.Raw)
			if err != nil {
				return nil, err
			}
			outIgn = ign3.Merge(outIgn, mergedIgn)
		}
	}

	// For file entries without a default overwrite, set it to true
	// The MCO will always overwrite any files, but Ignition will not,
	// Causing a difference in behaviour and failures when scaling new nodes into the cluster.
	// This was a default change from ign spec2->spec3 which users don't often specify.
	for idx := range outIgn.Storage.Files {
		if outIgn.Storage.Files[idx].Overwrite == nil {
			outIgn.Storage.Files[idx].Overwrite = boolToPtr(true)
		}
	}

	rawOutIgn, err := json.Marshal(outIgn)
	if err != nil {
		return nil, err
	}

	// Setting FIPS to true or kernelType to a non-default value in any MachineConfig takes priority in setting that field
	for _, cfg := range configs {
		if cfg.Spec.FIPS {
			fips = true
		}
		if cfg.Spec.KernelType == commonconsts.KernelTypeRealtime || cfg.Spec.KernelType == commonconsts.KernelType64kPages {
			kernelType = cfg.Spec.KernelType
		}
	}

	// If no MC sets kernelType, then set it to 'default' since that's what it is using
	if kernelType == "" {
		kernelType = commonconsts.KernelTypeDefault
	}

	kargs := []string{}
	for _, cfg := range configs {
		kargs = append(kargs, cfg.Spec.KernelArguments...)
	}

	extensions := []string{}
	for _, cfg := range configs {
		extensions = append(extensions, cfg.Spec.Extensions...)
	}

	// Ensure that kernel-devel extension is applied only with default kernel.
	if kernelType != commonconsts.KernelTypeDefault {
		if InSlice("kernel-devel", extensions) {
			return nil, fmt.Errorf("installing kernel-devel extension is not supported with kernelType: %s", kernelType)
		}
	}

	// For layering, we want to let the user override OSImageURL again
	// The template configs always match what's in controllerconfig because they get rendered from there,
	// so the only way we get an override here is if the user adds something different
	osImageURL := GetDefaultBaseImageContainer(&cconfig.Spec)
	for _, cfg := range configs {
		if cfg.Spec.OSImageURL != "" {
			osImageURL = cfg.Spec.OSImageURL
		}
	}

	// Allow overriding the extensions container
	baseOSExtensionsContainerImage := cconfig.Spec.BaseOSExtensionsContainerImage
	for _, cfg := range configs {
		if cfg.Spec.BaseOSExtensionsContainerImage != "" {
			baseOSExtensionsContainerImage = cfg.Spec.BaseOSExtensionsContainerImage
		}
	}

	return &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:                     osImageURL,
			BaseOSExtensionsContainerImage: baseOSExtensionsContainerImage,
			KernelArguments:                kargs,
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
			FIPS:       fips,
			KernelType: kernelType,
			Extensions: extensions,
		},
	}, nil
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	// Disable gosec here to avoid throwing
	// G306: Expect WriteFile permissions to be 0600 or less
	// #nosec
	os.WriteFile("/dev/termination-log", []byte(msg), 0o644)
	klog.Fatal(msg)
}

// InSlice search for an element in slice and return true if found, otherwise return false
func InSlice(elem string, slice []string) bool {
	for _, k := range slice {
		if k == elem {
			return true
		}
	}
	return false
}

// ValidateMachineConfig validates that given MachineConfig Spec is valid.
func ValidateMachineConfig(cfg mcfgv1.MachineConfigSpec) error {
	if !(cfg.KernelType == "" || cfg.KernelType == commonconsts.KernelTypeDefault || cfg.KernelType == commonconsts.KernelTypeRealtime || cfg.KernelType == commonconsts.KernelType64kPages) {
		return fmt.Errorf("kernelType=%s is invalid", cfg.KernelType)
	}

	if cfg.Config.Raw != nil {
		ignCfg, err := IgnParseWrapper(cfg.Config.Raw)
		if err != nil {
			return err
		}
		if err := ValidateIgnition(ignCfg); err != nil {
			return err
		}
	}
	return nil
}

// Validates that a given MachineConfig's extensions are supported.
func ValidateMachineConfigExtensions(cfg mcfgv1.MachineConfigSpec) error {
	return validateExtensions(cfg.Extensions)
}

func validateExtensions(exts []string) error {
	supportedExtensions := SupportedExtensions()
	invalidExts := []string{}
	for _, ext := range exts {
		if _, ok := supportedExtensions[ext]; !ok {
			invalidExts = append(invalidExts, ext)
		}
	}
	if len(invalidExts) != 0 {
		return fmt.Errorf("invalid extensions found: %v", invalidExts)
	}
	return nil
}

// Resolves a list of supported extensions to the individual packages required
// for each of those extensions. Returns an error is any of the supplied
// extensions is invalid.
func GetPackagesForSupportedExtensions(exts []string) ([]string, error) {
	if err := validateExtensions(exts); err != nil {
		return nil, err
	}

	pkgs := []string{}

	supported := SupportedExtensions()
	for _, ext := range exts {
		for _, pkg := range supported[ext] {
			pkgs = append(pkgs, pkg)
		}
	}

	return pkgs, nil
}

// Returns list of extensions possible to install on a CoreOS based system.
func SupportedExtensions() map[string][]string {
	// In future when list of extensions grow, it will make
	// more sense to populate it in a dynamic way.

	// These are RHCOS supported extensions.
	// Each extension keeps a list of packages required to get enabled on host.
	return map[string][]string{
		"wasm":                 {"crun-wasm"},
		"ipsec":                {"NetworkManager-libreswan", "libreswan"},
		"usbguard":             {"usbguard"},
		"kerberos":             {"krb5-workstation", "libkadm5"},
		"kernel-devel":         {"kernel-devel", "kernel-headers"},
		"sandboxed-containers": {"kata-containers"},
		"sysstat":              {"sysstat"},
	}
}

// GetManagedKey returns the managed key for sub-controllers, handling any migration needed
func GetManagedKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface, prefix, suffix, deprecatedKey string) (string, error) {
	managedKey := fmt.Sprintf("%s-%s-generated-%s", prefix, pool.Name, suffix)
	// if we don't have a client, we're installing brand new, and we don't need to adjust for backward compatibility
	if client == nil {
		return managedKey, nil
	}
	if _, err := client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{}); err == nil {
		return managedKey, nil
	}
	old, err := client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), deprecatedKey, metav1.GetOptions{})
	if err != nil && !kerr.IsNotFound(err) {
		return "", fmt.Errorf("could not get MachineConfig %q: %w", deprecatedKey, err)
	}
	// this means no previous CR config were here, so we can start fresh
	if kerr.IsNotFound(err) {
		return managedKey, nil
	}
	// if we're here, we'll grab the old CR config, dupe it and patch its name
	mc, err := MachineConfigFromRawIgnConfig(pool.Name, managedKey, old.Spec.Config.Raw)
	if err != nil {
		return "", err
	}
	_, err = client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	err = client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), deprecatedKey, metav1.DeleteOptions{})
	return managedKey, err
}

// GetDefaultBaseImageContainer returns the default bootable host base image.
func GetDefaultBaseImageContainer(cconfigspec *mcfgv1.ControllerConfigSpec) string {
	return cconfigspec.BaseOSContainerImage
}

// Configures common template FuncMaps used across all renderers.
func GetTemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"toString": strval,
		"indent":   indent,
	}
}

// Converts an interface to a string.
// Copied from: https://github.com/Masterminds/sprig/blob/master/strings.go
// Copied to remove the dependency on the Masterminds/sprig library.
func strval(v interface{}) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// Indents a string n spaces.
// Copied from: https://github.com/Masterminds/sprig/blob/master/strings.go
// Copied to remove the dependency on the Masterminds/sprig library.
func indent(spaces int, v string) string {
	pad := strings.Repeat(" ", spaces)
	return pad + strings.ReplaceAll(v, "\n", "\n"+pad)
}

// ioutil.ReadDir has been deprecated with os.ReadDir.
// ioutil.ReadDir() used to return []fs.FileInfo but os.ReadDir() returns []fs.DirEntry.
// Making it helper function so that we can reuse coversion of []fs.DirEntry into []fs.FileInfo
// Implementation to fetch fileInfo is taken from https://pkg.go.dev/io/ioutil#ReadDir
func ReadDir(path string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir %q: %w", path, err)
	}
	infos := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("failed to fetch fileInfo of %q in %q: %w", entry.Name(), path, err)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func NamespacedEventRecorder(delegate record.EventRecorder) record.EventRecorder {
	return namespacedEventRecorder{delegate: delegate}
}

type namespacedEventRecorder struct {
	delegate record.EventRecorder
}

func ensureEventNamespace(object runtime.Object) runtime.Object {
	orig, err := reference.GetReference(scheme.Scheme, object)
	if err != nil {
		return object
	}
	ret := orig.DeepCopy()
	if ret.Namespace == "" {
		// the ref must set a namespace to avoid going into default.
		// cluster operators are clusterscoped and "" becomes default.  Even though the clusteroperator
		// is not in this namespace, the logical namespace of this operator is the openshift-machine-config-operator.
		ret.Namespace = commonconsts.MCONamespace
	}

	return ret
}

var _ record.EventRecorder = namespacedEventRecorder{}

func (n namespacedEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	n.delegate.Event(ensureEventNamespace(object), eventtype, reason, message)
}

func (n namespacedEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	n.delegate.Eventf(ensureEventNamespace(object), eventtype, reason, messageFmt, args...)
}

func (n namespacedEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	n.delegate.AnnotatedEventf(ensureEventNamespace(object), annotations, eventtype, reason, messageFmt, args...)
}

func DoARebuild(pool *mcfgv1.MachineConfigPool) bool {
	_, ok := pool.Labels[commonconsts.RebuildPoolLabel]
	return ok

}

// isSubdirectory checks if targetPath is a subdirectory of dirPath.
func IsSubdirectory(dirPath, targetPath string) bool {
	// Clean and add trailing separator to dirPath to ensure proper matching
	dirPath = filepath.Clean(dirPath) + string(filepath.Separator)
	targetPath = filepath.Clean(targetPath)

	// Check if targetPath has dirPath as its prefix
	return strings.HasPrefix(targetPath, dirPath)
}

func FindClosestFilePolicyPathMatch(diffPath string, filePolicies []opv1.NodeDisruptionPolicyStatusFile) (bool, []opv1.NodeDisruptionPolicyStatusAction) {
	matchLength := 0
	matchFound := false
	matchActions := []opv1.NodeDisruptionPolicyStatusAction{}

	for _, filePolicy := range filePolicies {
		klog.V(4).Infof("comparing policy path %s to diff path %s", filePolicy.Path, diffPath)
		// Check if either of the following are true:
		// (i) if diffPath and filePolicy.Path are an exact match
		// (ii) if diffPath is a subdir of filePolicy.Path
		if (diffPath == filePolicy.Path) || IsSubdirectory(filePolicy.Path, diffPath) {
			// If a match was found, compare the length so the longest match is preserved
			if len(filePolicy.Path) > matchLength {
				matchFound = true
				matchLength = len(filePolicy.Path)
				matchActions = filePolicy.Actions
			}
		}
	}
	return matchFound, matchActions
}

// Extracts the minimum TLS version and cipher suites from apiServer object,
func GetSecurityProfileCiphersFromAPIServer(apiServer *configv1.APIServer) (string, []string) {
	// If no apiServer object exists, default to the intermediate profile by calling
	// GetSecurityProfileCiphers with a nil object for TLSSecurityProfile
	if apiServer == nil {
		return GetSecurityProfileCiphers(nil)
	}
	return GetSecurityProfileCiphers(apiServer.Spec.TLSSecurityProfile)
}

// Extracts the minimum TLS version and cipher suites from TLSSecurityProfile object,
// Converts the ciphers to IANA names as supported by Kube ServingInfo config.
// If profile is nil, returns config defined by the Intermediate TLS Profile
func GetSecurityProfileCiphers(profile *configv1.TLSSecurityProfile) (string, []string) {
	var profileType configv1.TLSProfileType
	if profile == nil {
		profileType = configv1.TLSProfileIntermediateType
	} else {
		profileType = profile.Type
	}

	var profileSpec *configv1.TLSProfileSpec
	if profileType == configv1.TLSProfileCustomType {
		if profile.Custom != nil {
			profileSpec = &profile.Custom.TLSProfileSpec
		}
	} else {
		profileSpec = configv1.TLSProfiles[profileType]
	}

	// nothing found / custom type set but no actual custom spec
	if profileSpec == nil {
		profileSpec = configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}

	// need to remap all Ciphers to their respective IANA names used by Go
	return string(profileSpec.MinTLSVersion), crypto.OpenSSLToIANACipherSuites(profileSpec.Ciphers)
}

// Converts tlsMinVersion and tlscipherSuites flags to a tlsConfig object that is used
// by the http.Server() call used in apiserver.NewAPIServer() & apiserver.Serve()
//
//nolint:gosec
func GetGoTLSConfig(tlsMinVersion string, tlscipherSuites []string) *tls.Config {
	// Create tlsConfig from arguments, using k8sapiflag for the uint16 translation
	tlsMinVersionID, tlsMinVersionErr := k8sapiflag.TLSVersion(tlsMinVersion)
	tlscipherSuiteIDs, tlscipherSuiteIDsErr := k8sapiflag.TLSCipherSuites(tlscipherSuites)
	// If any errors are encountered, log it and fallback to intermediate settings. This is very unlikely as tls arguments are guarded by API validation.
	if tlsMinVersionErr != nil || tlscipherSuiteIDsErr != nil || len(tlscipherSuiteIDs) == 0 {
		klog.Errorf("Error using provided tls arguments: tlsMinVersionErr: %v, tlscipherSuiteIDsErr: %v, tlscipherSuiteLength: %v", tlsMinVersionErr, tlscipherSuiteIDsErr, len(tlscipherSuiteIDs))
		tlsMinVersionID, _ = k8sapiflag.TLSVersion(string(configv1.TLSProfiles[configv1.TLSProfileIntermediateType].MinTLSVersion))
		tlscipherSuiteIDs, _ = k8sapiflag.TLSCipherSuites(configv1.TLSProfiles[configv1.TLSProfileIntermediateType].Ciphers)
	}
	// This causes a G402: TLS MinVersion too low. (gosec) verify error, possibly because the version is determined at runtime?
	return &tls.Config{MinVersion: tlsMinVersionID, CipherSuites: tlscipherSuiteIDs}
}

func GetBootstrapAPIServer() (*configv1.APIServer, error) {
	apiserverData, err := os.ReadFile(commonconsts.APIServerBootstrapFileLocation)
	if os.IsNotExist(err) {
		// This is not an error; it just means that an APIServer manifest was not provided at install time
		klog.Infof("No bootstrap apiserver manifest found, bootstrap MCS will use defaults")
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("error getting apiserver from disk: %w", err)
	}
	klog.Infof("Reading in bootstrap apiserver manifest was successful")
	apiserver := new(configv1.APIServer)
	if err := yaml.Unmarshal(apiserverData, &apiserver); err != nil {
		return nil, fmt.Errorf("unmarshal into apiserver failed %w", err)
	}
	return apiserver, nil
}

const (
	// OSLabel is used to identify which type of OS the node has
	OSLabel = "kubernetes.io/os"
)

func GetNodesForPool(mcpLister v1.MachineConfigPoolLister, nodeLister corev1listers.NodeLister, pool *mcfgv1.MachineConfigPool) ([]*corev1.Node, error) {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	initialNodes, err := nodeLister.List(selector)
	if err != nil {
		return nil, err
	}

	nodes := []*corev1.Node{}
	for _, n := range initialNodes {
		p, err := GetPrimaryPoolForNode(mcpLister, n)
		if err != nil {
			klog.Warningf("can't get pool for node %q: %v", n.Name, err)
			continue
		}
		if p == nil {
			continue
		}
		if p.Name != pool.Name {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func GetPrimaryPoolForNode(mcpLister v1.MachineConfigPoolLister, node *corev1.Node) (*mcfgv1.MachineConfigPool, error) {
	pools, _, err := GetPoolsForNode(mcpLister, node)
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return nil, nil
	}
	return pools[0], nil
}

func GetPoolsForNode(mcpLister v1.MachineConfigPoolLister, node *corev1.Node) ([]*mcfgv1.MachineConfigPool, *int, error) {
	var metricValue int
	master, worker, custom, err := ListPools(node, mcpLister)
	if err != nil {
		return nil, nil, err
	}
	if master == nil && custom == nil && worker == nil {
		return nil, nil, nil
	}

	switch {
	case len(custom) > 1:
		return nil, nil, fmt.Errorf("node %s belongs to %d custom roles, cannot proceed with this Node", node.Name, len(custom))
	case len(custom) == 1:
		pls := []*mcfgv1.MachineConfigPool{}
		if master != nil {
			// if we have a custom pool and master, defer to master and return.
			klog.Infof("Found master node that matches selector for custom pool %v, defaulting to master. This node will not have any custom role configuration as a result. Please review the node to make sure this is intended", custom[0].Name)
			metricValue = 1
			pls = append(pls, master)
		} else {
			metricValue = 0
			pls = append(pls, custom[0])
		}
		if worker != nil {
			pls = append(pls, worker)
		}
		// this allows us to have master, worker, infra but be in the master pool.
		// or if !worker and !master then we just use the custom pool.
		return pls, &metricValue, nil
	case master != nil:
		// In the case where a node is both master/worker, have it live under
		// the master pool. This occurs in CodeReadyContainers and general
		// "single node" deployments, which one may want to do for testing bare
		// metal, etc.
		metricValue = 0
		return []*mcfgv1.MachineConfigPool{master}, &metricValue, nil
	default:
		// Otherwise, it's a worker with no custom roles.
		metricValue = 0
		return []*mcfgv1.MachineConfigPool{worker}, &metricValue, nil
	}
}

func GetPoolNamesForNode(mcpLister v1.MachineConfigPoolLister, node *corev1.Node) ([]string, error) {
	pools, _, err := GetPoolsForNode(mcpLister, node)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, pool := range pools {
		names = append(names, pool.Name)
	}
	return names, nil
}

// isWindows checks if given node is a Windows node or a Linux node
func IsWindows(node *corev1.Node) bool {
	windowsOsValue := "windows"
	if value, ok := node.ObjectMeta.Labels[OSLabel]; ok {
		if value == windowsOsValue {
			return true
		}
		return false
	}
	// All the nodes should have a OS label populated by kubelet, if not just to maintain
	// backwards compatibility, we can returning true here.
	return false
}

func ListPools(node *corev1.Node, mcpLister v1.MachineConfigPoolLister) (*mcfgv1.MachineConfigPool, *mcfgv1.MachineConfigPool, []*mcfgv1.MachineConfigPool, error) {
	if IsWindows(node) {
		// This is not an error, is this a Windows Node and it won't be managed by MCO. We're explicitly logging
		// here at a high level to disambiguate this from other pools = nil  scenario
		klog.V(4).Infof("Node %v is a windows node so won't be managed by MCO", node.Name)
		return nil, nil, nil, nil
	}
	pl, err := mcpLister.List(labels.Everything())
	if err != nil {
		return nil, nil, nil, err
	}

	var pools []*mcfgv1.MachineConfigPool
	for _, p := range pl {
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid label selector: %w", err)
		}

		// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(node.Labels)) {
			continue
		}

		pools = append(pools, p)
	}

	if len(pools) == 0 {
		// This is not an error, as there might be nodes in cluster that are not managed by machineconfigpool.
		return nil, nil, nil, nil
	}

	var master, worker *mcfgv1.MachineConfigPool
	var custom []*mcfgv1.MachineConfigPool
	for _, pool := range pools {
		switch pool.Name {
		case commonconsts.MachineConfigPoolMaster:
			master = pool
		case commonconsts.MachineConfigPoolWorker:
			worker = pool
		default:
			custom = append(custom, pool)
		}
	}

	return master, worker, custom, nil
}

// IsCoreOSNode checks whether the pretty name of a node matches any of the
// coreos based image names
func IsCoreOSNode(node *corev1.Node) bool {
	validOSImages := []string{osrelease.RHCOS, osrelease.FCOS, osrelease.SCOS}

	for _, img := range validOSImages {
		if strings.Contains(node.Status.NodeInfo.OSImage, img) {
			return true
		}
	}
	return false
}
