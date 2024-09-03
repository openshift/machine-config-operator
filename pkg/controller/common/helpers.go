package common

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"strings"
	"text/template"

	"github.com/ghodss/yaml"
	kerr "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/openshift/machine-config-operator/pkg/controller/common/configs"
	"github.com/openshift/machine-config-operator/pkg/controller/common/constants"
)

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
	mc, err := configs.MachineConfigFromRawIgnConfig(pool.Name, managedKey, old.Spec.Config.Raw)
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
		ret.Namespace = constants.MCONamespace
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
	_, ok := pool.Labels[constants.RebuildPoolLabel]
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
	apiserverData, err := os.ReadFile(constants.APIServerBootstrapFileLocation)
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
