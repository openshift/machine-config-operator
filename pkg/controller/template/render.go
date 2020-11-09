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
	"github.com/golang/glog"
	configv1 "github.com/openshift/api/config/v1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
)

// RenderConfig is wrapper around ControllerConfigSpec.
type RenderConfig struct {
	*mcfgv1.ControllerConfigSpec
	PullSecret string
}

const (
	filesDir       = "files"
	unitsDir       = "units"
	platformBase   = "_base"
	platformOnPrem = "on-prem"
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

func platformStringFromControllerConfigSpec(ic *mcfgv1.ControllerConfigSpec) (string, error) {
	if ic.Infra == nil {
		ic.Infra = &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{},
		}
	}

	if ic.Infra.Status.PlatformStatus == nil {
		ic.Infra.Status.PlatformStatus = &configv1.PlatformStatus{
			Type: "",
		}
	}

	switch ic.Infra.Status.PlatformStatus.Type {
	case "":
		// if Platform is nil, return nil platform and an error message
		return "", fmt.Errorf("cannot generate MachineConfigs when no platformStatus.type is set")
	case platformBase:
		return "", fmt.Errorf("platform _base unsupported")
	case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.BareMetalPlatformType, configv1.GCPPlatformType, configv1.OpenStackPlatformType, configv1.LibvirtPlatformType, configv1.OvirtPlatformType, configv1.VSpherePlatformType, configv1.KubevirtPlatformType, configv1.NonePlatformType:
		return strings.ToLower(string(ic.Infra.Status.PlatformStatus.Type)), nil
	default:
		// platformNone is used for a non-empty, but currently unsupported platform.
		// This allows us to incrementally roll out new platforms across the project
		// by provisioning platforms before all support is added.
		glog.Warningf("Warning: the controller config referenced an unsupported platform: %v", ic.Infra.Status.PlatformStatus.Type)

		return strings.ToLower(string(configv1.NonePlatformType)), nil
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
	platformString, err := platformStringFromControllerConfigSpec(config.ControllerConfigSpec)
	if err != nil {
		return nil, err
	}

	platformDirs := []string{}
	if !*commonAdded {
		// Loop over templates/common which applies everywhere
		for _, dir := range []string{platformBase, platformOnPrem, platformString} {
			if dir == platformOnPrem && !onPremPlatform(config.Infra.Status.PlatformStatus.Type) {
				continue
			}
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
	for _, dir := range []string{platformBase, platformOnPrem, platformString} {
		if dir == platformOnPrem && !onPremPlatform(config.Infra.Status.PlatformStatus.Type) {
			continue
		}
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

	ignCfg, err := ctrlcommon.TranspileCoreOSConfigToIgn(keySortVals(files), keySortVals(units))
	if err != nil {
		return nil, fmt.Errorf("error transpiling CoreOS config to Ignition config: %v", err)
	}
	mcfg, err := ctrlcommon.MachineConfigFromIgnConfig(role, name, ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating MachineConfig from Ignition config: %v", err)
	}
	// And inject the osimageurl here
	mcfg.Spec.OSImageURL = config.OSImageURL

	return mcfg, nil
}

// renderTemplate renders a template file with values from a RenderConfig
// returns the rendered file data
func renderTemplate(config RenderConfig, path string, b []byte) ([]byte, error) {
	funcs := sprig.TxtFuncMap()
	funcs["skip"] = skipMissing
	funcs["cloudProvider"] = cloudProvider
	funcs["cloudConfigFlag"] = cloudConfigFlag
	funcs["onPremPlatformAPIServerInternalIP"] = onPremPlatformAPIServerInternalIP
	funcs["onPremPlatformIngressIP"] = onPremPlatformIngressIP
	funcs["onPremPlatformShortName"] = onPremPlatformShortName
	funcs["onPremPlatformKeepalivedEnableUnicast"] = onPremPlatformKeepalivedEnableUnicast
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

func cloudProvider(cfg RenderConfig) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.OpenStackPlatformType, configv1.VSpherePlatformType:
			return strings.ToLower(string(cfg.Infra.Status.PlatformStatus.Type)), nil
		case configv1.GCPPlatformType:
			return "gce", nil
		default:
			return "", nil
		}
	} else {
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

	if cfg.Infra == nil {
		cfg.Infra = &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{},
		}
	}

	if cfg.Infra.Status.PlatformStatus == nil {
		cfg.Infra.Status.PlatformStatus = &configv1.PlatformStatus{
			Type: "",
		}
	}

	flag := "--cloud-config=/etc/kubernetes/cloud.conf"
	switch cfg.Infra.Status.PlatformStatus.Type {
	case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType, configv1.OpenStackPlatformType, configv1.VSpherePlatformType:
		return flag
	default:
		return ""
	}
}

func onPremPlatformShortName(cfg RenderConfig) interface{} {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return "kni"
		case configv1.OvirtPlatformType:
			return "ovirt"
		case configv1.OpenStackPlatformType:
			return "openstack"
		case configv1.VSpherePlatformType:
			return "vsphere"
		case configv1.KubevirtPlatformType:
			return "kubevirt"
		default:
			return ""
		}
	} else {
		return ""
	}
}

func onPremPlatformKeepalivedEnableUnicast(cfg RenderConfig) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType, configv1.KubevirtPlatformType:
			return "yes", nil
		default:
			return "no", nil
		}
	} else {
		return "no", nil
	}
}

func onPremPlatformIngressIP(cfg RenderConfig) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.IngressIP, nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.IngressIP, nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.IngressIP, nil
		case configv1.KubevirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Kubevirt.IngressIP, nil
		case configv1.VSpherePlatformType:
			if cfg.Infra.Status.PlatformStatus.VSphere != nil {
				return cfg.Infra.Status.PlatformStatus.VSphere.IngressIP, nil
			}
			// VSphere UPI doesn't populate VSphere field. So it's not an error,
			// and there is also no data
			return nil, nil
		default:
			return nil, fmt.Errorf("invalid platform for Ingress IP")
		}
	} else {
		return nil, fmt.Errorf("")
	}
}

func onPremPlatformAPIServerInternalIP(cfg RenderConfig) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.APIServerInternalIP, nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.APIServerInternalIP, nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.APIServerInternalIP, nil
		case configv1.VSpherePlatformType:
			if cfg.Infra.Status.PlatformStatus.VSphere != nil {
				return cfg.Infra.Status.PlatformStatus.VSphere.APIServerInternalIP, nil
			}
			// VSphere UPI doesn't populate VSphere field. So it's not an error,
			// and there is also no data
			return nil, nil
		case configv1.KubevirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Kubevirt.APIServerInternalIP, nil
		default:
			return nil, fmt.Errorf("invalid platform for API Server Internal IP")
		}
	} else {
		return nil, fmt.Errorf("")
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

func onPremPlatform(platformString configv1.PlatformType) bool {
	switch platformString {
	case configv1.BareMetalPlatformType, configv1.OvirtPlatformType, configv1.OpenStackPlatformType, configv1.VSpherePlatformType, configv1.KubevirtPlatformType:
		return true
	default:
		return false
	}
}
