package template

import (
	"bytes"
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
	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RenderConfig is wrapper around ControllerConfigSpec.
type RenderConfig struct {
	*mcfgv1.ControllerConfigSpec
	PullSecret string
}

const (
	filesDir = "files"
	unitsDir = "units"

	platformAWS       = "aws"
	platformAzure     = "azure"
	platformOpenstack = "openstack"
	platformLibvirt   = "libvirt"
	platformNone      = "none"
	platformVSphere   = "vsphere"
	platformBase      = "_base"
)

// generateTemplateMachineConfigs returns MachineConfig objects from the templateDir and a config object
// expected directory structure for correctly templating machine configs: <templatedir>/<role>/<name>/<platform>/<type>/<tmpl_file>
//
// All files from platform _base are always included, and may be overridden or
// supplemented by platform-specific templates
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
		path := filepath.Join(templateDir, role)
		roleConfigs, err := GenerateMachineConfigsForRole(config, role, path)
		if err != nil {
			return nil, fmt.Errorf("failed to create MachineConfig for role %s: %v", role, err)
		}
		cfgs = append(cfgs, roleConfigs...)
	}

	// tag all the machineconfigs with version of the controller.
	for _, cfg := range cfgs {
		if cfg.Annotations == nil {
			cfg.Annotations = map[string]string{}
		}
		cfg.Annotations[common.GeneratedByControllerVersionAnnotationKey] = version.Version.String()
	}

	return cfgs, nil
}

// GenerateMachineConfigsForRole creates MachineConfigs for the role provided
func GenerateMachineConfigsForRole(config *RenderConfig, role string, path string) ([]*mcfgv1.MachineConfig, error) {
	infos, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir %q: %v", path, err)
	}

	cfgs := []*mcfgv1.MachineConfig{}
	for _, info := range infos {
		if !info.IsDir() {
			glog.Infof("ignoring non-directory path %q", info.Name())
			continue
		}
		name := info.Name()
		namePath := filepath.Join(path, name)
		nameConfig, err := generateMachineConfigForName(config, role, name, namePath)
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
		return "", fmt.Errorf("cannot generateMachineConfigs with an empty platform field")
	case platformBase:
		return "", fmt.Errorf("platform _base unsupported")
	case platformAWS, platformAzure, platformOpenstack, platformLibvirt, platformNone:
		// TODO: these constants are wrong, they should match what is reported by the infrastructure provider
		return ic.Platform, nil
	default:
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

func generateMachineConfigForName(config *RenderConfig, role, name, path string) (*mcfgv1.MachineConfig, error) {
	platform, err := platformFromControllerConfigSpec(config.ControllerConfigSpec)
	if err != nil {
		return nil, err
	}

	platformDirs := []string{}
	for _, dir := range []string{platformBase, platform} {
		platformPath := filepath.Join(path, dir)
		exists, err := existsDir(platformPath)
		if err != nil {
			return nil, err
		}
		if !exists {
			glog.Errorf("could not find expected template directory %s", platformPath)
			return nil, fmt.Errorf("platform %s unsupported", config.Platform)
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

	mcfg := MachineConfigFromIgnConfig(role, name, ignCfg)
	// And inject the osimageurl here
	mcfg.Spec.OSImageURL = config.OSImageURL

	return mcfg, nil
}

const (
	machineConfigRoleLabelKey = "machineconfiguration.openshift.io/role"
)

// MachineConfigFromIgnConfig creates a MachineConfig with the provided Ignition config
func MachineConfigFromIgnConfig(role string, name string, ignCfg *ignv2_2types.Config) *mcfgv1.MachineConfig {
	labels := map[string]string{
		machineConfigRoleLabelKey: role,
	}
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Name:   name,
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "",
			Config:     *ignCfg,
		},
	}
}

func transpileToIgn(files, units []string) (*ignv2_2types.Config, error) {
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

	ignCfg, rep := ctconfig.Convert(ctCfg, "", nil)
	if rep.IsFatal() {
		return nil, fmt.Errorf("failed to convert config to Ignition config %s", rep)
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
	funcs["cloudProvider"] = cloudProvider
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

// Keys labled with skip ie. {{skip "key"}}, don't need to be templated in now because at Ignition request they will be templated in with query params
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
		"etcd.kube-system.svc",               // sign for the local etcd service name that cluster-network apiservers use to communicate
		"etcd.kube-system.svc.cluster.local", // sign for the local etcd service name that cluster-network apiservers use to communicate
		"${ETCD_DNS_NAME}",
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

func cloudProvider(cfg RenderConfig) (interface{}, error) {
	switch cfg.Platform {
	case platformAWS:
		return platformAWS, nil
	case platformAzure:
		return platformAzure, nil
	case platformOpenstack:
		return platformOpenstack, nil
	case platformVSphere:
		return platformVSphere, nil
	}
	return "", nil
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
		return false, fmt.Errorf("expected template directory %q is not a directory", path)
	}
	return true, nil
}
