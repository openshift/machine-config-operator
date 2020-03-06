package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	ctconfig "github.com/coreos/container-linux-config-transpiler/config"
	cttypes "github.com/coreos/container-linux-config-transpiler/config/types"
	ign "github.com/coreos/ignition/config/v2_4"
	igntypes "github.com/coreos/ignition/config/v2_4/types"
	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// RenderConfig is wrapper around ControllerConfigSpec.
type RenderConfig struct {
	*mcfgv1.ControllerConfigSpec
	PullSecret string
}

const (
	filesDir = "files"
	unitsDir = "units"

	// TODO: these constants are wrong, they should match what is reported by the infrastructure provider
	platformAWS       = "aws"
	platformAzure     = "azure"
	platformBaremetal = "baremetal"
	platformGCP       = "gcp"
	platformOpenStack = "openstack"
	platformLibvirt   = "libvirt"
	platformNone      = "none"
	platformVSphere   = "vsphere"
	platformBase      = "_base"
	platformOvirt     = "ovirt"
)

// generateTemplateMachineConfigs returns MachineConfig objects from the templateDir and a config object
// expected directory structure for correctly templating machine configs: <templatedir>/<role>/<name>/<platform>/<type>/<tmpl_file>
//
// All files from platform _base are always included, and may be overridden or
// supplemented by platform-specific templates.
//
//  ex:
//       templates/worker/00-worker/_base/units/kubelet.conf.tmpl
//                                    /files/hostname.tmpl
//                              /aws/units/kubelet-dropin.conf.tmpl
//                       /01-worker-kubelet/_base/files/random.conf.tmpl
//                /master/00-master/_base/units/kubelet.tmpl
//                                    /files/hostname.tmpl
//
func generateTemplateMachineConfigs(config *RenderConfig, templateDir string) ([]*mcfgv1.MachineConfig, error) {
	infos, err := ioutil.ReadDir(templateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir %q: %v", templateDir, err)
	}

	cfgs := []*mcfgv1.MachineConfig{}

	for _, info := range infos {
		if !info.IsDir() {
			glog.Infof("ignoring non-directory path %q", info.Name())
			continue
		}
		role := info.Name()
		if role == "common" {
			continue
		}

		roleConfigs, err := GenerateMachineConfigsForRole(config, role, templateDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create MachineConfig for role %s: %v", role, err)
		}
		cfgs = append(cfgs, roleConfigs...)
	}

	// tag all machineconfigs with the controller version
	for _, cfg := range cfgs {
		if cfg.Annotations == nil {
			cfg.Annotations = map[string]string{}
		}
		cfg.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = version.Hash
	}

	return cfgs, nil
}

// GenerateMachineConfigsForRole creates MachineConfigs for the role provided
func GenerateMachineConfigsForRole(config *RenderConfig, role, templateDir string) ([]*mcfgv1.MachineConfig, error) {
	rolePath := role
	//nolint:goconst
	if role != "worker" && role != "master" {
		// custom pools are only allowed to be worker's children
		// and can reuse the worker templates
		rolePath = "worker"
	}

	path := filepath.Join(templateDir, rolePath)
	infos, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir %q: %v", path, err)
	}

	cfgs := []*mcfgv1.MachineConfig{}
	// This func doesn't process "common"
	// common templates are only added to 00-<role>
	// templates/<role>/{00-<role>,01-<role>-container-runtime,01-<role>-kubelet}
	var commonAdded bool
	for _, info := range infos {
		if !info.IsDir() {
			glog.Infof("ignoring non-directory path %q", info.Name())
			continue
		}
		name := info.Name()
		namePath := filepath.Join(path, name)
		nameConfig, err := generateMachineConfigForName(config, role, name, templateDir, namePath, &commonAdded)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, nameConfig)
	}

	return cfgs, nil
}

func platformFromControllerConfigSpec(ic *mcfgv1.ControllerConfigSpec) (string, error) {
	switch ic.Platform {
	case "":
		// if Platform is nil, return nil platform and an error message
		return "", fmt.Errorf("cannot generate MachineConfigs when no platform is set")
	case platformBase:
		return "", fmt.Errorf("platform _base unsupported")
	case platformAWS, platformAzure, platformBaremetal, platformGCP, platformOpenStack, platformLibvirt, platformOvirt, platformVSphere, platformNone:
		return ic.Platform, nil
	default:
		// platformNone is used for a non-empty, but currently unsupported platform.
		// This allows us to incrementally roll out new platforms across the project
		// by provisioning platforms before all support is added.
		glog.Warningf("Warning: the controller config referenced an unsupported platform: %s", ic.Platform)
		return platformNone, nil
	}
}

func filterTemplates(toFilter map[string]string, path string, config *RenderConfig) error {
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		// empty templates signify don't create
		if info.Size() == 0 {
			delete(toFilter, info.Name())
			return nil
		}

		filedata, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read file %q: %v", path, err)
		}

		// Render the template file
		renderedData, err := renderTemplate(*config, path, filedata)
		if err != nil {
			return err
		}
		toFilter[info.Name()] = string(renderedData)
		return nil
	}

	return filepath.Walk(path, walkFn)
}

func generateMachineConfigForName(config *RenderConfig, role, name, templateDir, path string, commonAdded *bool) (*mcfgv1.MachineConfig, error) {
	platform, err := platformFromControllerConfigSpec(config.ControllerConfigSpec)
	if err != nil {
		return nil, err
	}

	platformDirs := []string{}
	if !*commonAdded {
		// Loop over templates/common which applies everywhere
		for _, dir := range []string{platformBase, platform} {
			basePath := filepath.Join(templateDir, "common", dir)
			exists, err := existsDir(basePath)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
			platformDirs = append(platformDirs, basePath)
		}
		*commonAdded = true
	}
	// And now over the target e.g. templates/master/00-master,01-master-container-runtime,01-master-kubelet
	for _, dir := range []string{platformBase, platform} {
		platformPath := filepath.Join(path, dir)
		exists, err := existsDir(platformPath)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		platformDirs = append(platformDirs, platformPath)
	}

	files := map[string]string{}
	units := map[string]string{}
	// walk all role dirs, with later ones taking precedence
	for _, platformDir := range platformDirs {
		p := filepath.Join(platformDir, filesDir)
		exists, err := existsDir(p)
		if err != nil {
			return nil, err
		}
		if exists {
			if err := filterTemplates(files, p, config); err != nil {
				return nil, err
			}
		}

		p = filepath.Join(platformDir, unitsDir)
		exists, err = existsDir(p)
		if err != nil {
			return nil, err
		}
		if exists {
			if err := filterTemplates(units, p, config); err != nil {
				return nil, err
			}
		}
	}

	// keySortVals returns a list of values, sorted by key
	// we need the lists of files and units to have a stable ordering for the checksum
	keySortVals := func(m map[string]string) []string {
		ks := []string{}
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)

		vs := []string{}
		for _, k := range ks {
			vs = append(vs, m[k])
		}

		return vs
	}

	ignCfg, err := transpileToIgn(keySortVals(files), keySortVals(units))
	if err != nil {
		return nil, fmt.Errorf("error transpiling ct config to Ignition config: %v", err)
	}
	mcfg, err := MachineConfigFromIgnConfig(role, name, ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating MachineConfig from Ignition config: %v", err)
	}
	// And inject the osimageurl here
	mcfg.Spec.OSImageURL = config.OSImageURL

	return mcfg, nil
}

// MachineConfigFromIgnConfig creates a MachineConfig with the provided Ignition config
func MachineConfigFromIgnConfig(role, name string, ignCfg interface{}) (*mcfgv1.MachineConfig, error) {
	rawIgnCfg, err := json.Marshal(ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error marshalling Ignition config: %v", err)
	}
	labels := map[string]string{
		mcfgv1.MachineConfigRoleLabelKey: role,
	}
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Name:   name,
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "",
			Config: runtime.RawExtension{
				Raw: rawIgnCfg,
			},
		},
	}, nil
}

func transpileToIgn(files, units []string) (*igntypes.Config, error) {
	var ctCfg cttypes.Config

	// Convert data to Ignition resources
	for _, d := range files {
		f := new(cttypes.File)
		if err := yaml.Unmarshal([]byte(d), f); err != nil {
			return nil, fmt.Errorf("failed to unmarshal file into struct: %v", err)
		}

		// Add the file to the config
		ctCfg.Storage.Files = append(ctCfg.Storage.Files, *f)
	}

	for _, d := range units {
		u := new(cttypes.SystemdUnit)
		if err := yaml.Unmarshal([]byte(d), u); err != nil {
			return nil, fmt.Errorf("failed to unmarshal systemd unit into struct: %v", err)
		}

		// Add the unit to the config
		ctCfg.Systemd.Units = append(ctCfg.Systemd.Units, *u)
	}

	ignCfgV2_2, rep := ctconfig.Convert(ctCfg, "", nil)
	if rep.IsFatal() {
		return nil, fmt.Errorf("failed to convert config to Ignition config %s", rep)
	}

	// Hack to convert spec 2.2 config to spec 2.4
	rawIgnCfg, err := json.Marshal(ignCfgV2_2)
	if err != nil {
		return nil, fmt.Errorf("SHOULD NEVER HAPPEN: failed to marshal Ignition spec v2.2 config %s", err)
	}
	ignCfg, rep, err := ign.Parse(rawIgnCfg)
	if rep.IsFatal() || err != nil {
		return nil, fmt.Errorf("SHOULD NEVER HAPPEN: failed to parse Ignition spec v2.4 config: %s\nreport: %v", err, rep)
	}

	return &ignCfg, nil
}

// renderTemplate renders a template file with values from a RenderConfig
// returns the rendered file data
func renderTemplate(config RenderConfig, path string, b []byte) ([]byte, error) {
	funcs := sprig.TxtFuncMap()
	funcs["skip"] = skipMissing
	funcs["etcdServerCertDNSNames"] = etcdServerCertDNSNames
	funcs["etcdPeerCertDNSNames"] = etcdPeerCertDNSNames
	funcs["etcdServerCertCommand"] = etcdServerCertCommand
	funcs["etcdPeerCertCommand"] = etcdPeerCertCommand
	funcs["etcdMetricCertCommand"] = etcdMetricCertCommand
	funcs["cloudProvider"] = cloudProvider
	funcs["cloudConfigFlag"] = cloudConfigFlag
	tmpl, err := template.New(path).Funcs(funcs).Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %s: %v", path, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, config); err != nil {
		return nil, fmt.Errorf("failed to execute template: %v", err)
	}

	return buf.Bytes(), nil
}

var skipKeyValidate = regexp.MustCompile(`^[_a-z]\w*$`)

// Keys labelled with skip ie. {{skip "key"}}, don't need to be templated in now because at Ignition request they will be templated in with query params
func skipMissing(key string) (interface{}, error) {
	if !skipKeyValidate.Match([]byte(key)) {
		return nil, fmt.Errorf("invalid key for skipKey")
	}

	return fmt.Sprintf("{{.%s}}", key), nil
}

// Process the {{etcdPeerCertDNSNames}} and {{etcdServerCertDNSNames}}
func etcdServerCertDNSNames(cfg RenderConfig) (interface{}, error) {
	var dnsNames = []string{
		"localhost",
		"etcd.kube-system.svc",                  // sign for the local etcd service name that cluster-network apiservers use to communicate
		"etcd.kube-system.svc.cluster.local",    // sign for the local etcd service name that cluster-network apiservers use to communicate
		"etcd.openshift-etcd.svc",               // sign for the local etcd service name that cluster-network apiservers use to communicate
		"etcd.openshift-etcd.svc.cluster.local", // sign for the local etcd service name that cluster-network apiservers use to communicate
		"${ETCD_WILDCARD_DNS_NAME}",
	}
	return strings.Join(dnsNames, ","), nil
}

func etcdPeerCertDNSNames(cfg RenderConfig) (interface{}, error) {
	if cfg.EtcdDiscoveryDomain == "" {
		return nil, fmt.Errorf("invalid configuration")
	}

	var dnsNames = []string{
		"${ETCD_DNS_NAME}",
		cfg.EtcdDiscoveryDomain, // https://github.com/etcd-io/etcd/blob/583763261f1c843e07c1bf7fea5fb4cfb684fe87/Documentation/op-guide/clustering.md#dns-discovery
	}
	return strings.Join(dnsNames, ","), nil
}

func etcdServerCertCommand(cfg RenderConfig) (interface{}, error) {
	commands := []string{}
	if cfg.Images[ClusterEtcdOperatorImageKey] == "" {
		serverCertDNS, err := etcdServerCertDNSNames(cfg)
		if err != nil {
			return nil, err
		}
		commands = append(commands, []string{
			"kube-client-agent \\",
			"  request \\",
			"  --kubeconfig=/etc/kubernetes/kubeconfig \\",
			"  --orgname=system:etcd-servers \\",
			"  --assetsdir=/etc/ssl/etcd \\",
			fmt.Sprintf("  --dnsnames=%s \\", serverCertDNS),
			"  --commonname=system:etcd-server:${ETCD_DNS_NAME} \\",
			"  --ipaddrs=${ETCD_IPV4_ADDRESS},${ETCD_LOCALHOST_IP} \\",
		}...)
	} else {
		commands = append(commands, []string{
			"cluster-etcd-operator \\",
			"  mount \\",
			"  --assetsdir=/etc/ssl/etcd \\",
			"  --commonname=system:etcd-server:${ETCD_DNS_NAME} \\",
		}...)
	}
	return commands, nil
}

func etcdPeerCertCommand(cfg RenderConfig) (interface{}, error) {
	commands := []string{}
	if cfg.Images[ClusterEtcdOperatorImageKey] == "" {
		peerCertDNS, err := etcdPeerCertDNSNames(cfg)
		if err != nil {
			return nil, err
		}
		commands = append(commands, []string{
			"kube-client-agent \\",
			"  request \\",
			"  --kubeconfig=/etc/kubernetes/kubeconfig \\",
			"  --orgname=system:etcd-peers \\",
			"  --assetsdir=/etc/ssl/etcd \\",
			fmt.Sprintf("  --dnsnames=%s \\", peerCertDNS),
			"  --commonname=system:etcd-peer:${ETCD_DNS_NAME} \\",
			"  --ipaddrs=${ETCD_IPV4_ADDRESS} \\",
		}...)
	} else {
		commands = append(commands, []string{
			"cluster-etcd-operator \\",
			"  mount \\",
			"  --assetsdir=/etc/ssl/etcd \\",
			"  --commonname=system:etcd-peer:${ETCD_DNS_NAME} \\",
		}...)
	}
	return commands, nil
}

func etcdMetricCertCommand(cfg RenderConfig) (interface{}, error) {
	commands := []string{}
	if cfg.Images[ClusterEtcdOperatorImageKey] == "" {
		metricCertDNS, err := etcdServerCertDNSNames(cfg)
		if err != nil {
			return nil, err
		}
		commands = append(commands, []string{
			"kube-client-agent \\",
			"  request \\",
			"  --kubeconfig=/etc/kubernetes/kubeconfig \\",
			"  --orgname=system:etcd-metrics \\",
			"  --assetsdir=/etc/ssl/etcd \\",
			fmt.Sprintf("  --dnsnames=%s \\", metricCertDNS),
			"  --commonname=system:etcd-metric:${ETCD_DNS_NAME} \\",
			"  --ipaddrs=${ETCD_IPV4_ADDRESS} \\",
		}...)
	} else {
		commands = append(commands, []string{
			"cluster-etcd-operator \\",
			"  mount \\",
			"  --assetsdir=/etc/ssl/etcd \\",
			"  --commonname=system:etcd-metric:${ETCD_DNS_NAME} \\",
		}...)
	}
	return commands, nil
}

func cloudProvider(cfg RenderConfig) (interface{}, error) {
	switch cfg.Platform {
	case platformAWS, platformAzure, platformOpenStack, platformVSphere:
		return cfg.Platform, nil
	case platformGCP:
		return "gce", nil
	default:
		return "", nil
	}
}

// Process the {{cloudConfigFlag .}}
// If the CloudProviderConfig field is set and not empty, this
// returns the cloud conf flag for kubelet [1] pointing the kubelet to use
// /etc/kubernetes/cloud.conf for configuring the cloud provider for select platforms.
// By default, even if CloudProviderConfig fields is set, the kubelet will be configured to be
// used for select platforms only.
//
// [1]: https://kubernetes.io/docs/reference/command-line-tools-reference/kubelet/#options
func cloudConfigFlag(cfg RenderConfig) interface{} {
	if cfg.CloudProviderConfig == "" {
		return ""
	}
	flag := "--cloud-config=/etc/kubernetes/cloud.conf"
	switch cfg.Platform {
	case platformAzure, platformOpenStack:
		return flag
	default:
		return ""
	}
}

// existsDir returns true if path exists and is a directory, false if the path
// does not exist, and error if there is a runtime error or the path is not a directory
func existsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open dir %q: %v", path, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("expected template directory, %q is not a directory", path)
	}
	return true, nil
}
