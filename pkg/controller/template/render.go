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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// renderConfig is wrapper around ControllerConfigSpec.
type renderConfig struct {
	*mcfgv1.ControllerConfigSpec
}

const (
	filesDir = "files"
	unitsDir = "units"
)

// generateMachineConfigs returns MachineConfig objects from the templateDir and a config object
// expected directory structure for correctly templating machine configs: <templatedir>/<platform>/<role>/<type>/<tmpl_file>
//
// All files from platform _base are always included, and may be overridden or
// supplemented by platform-specific templates
//
//  ex:
//       templates/_base/worker/units/kubelet.conf.tmpl
//                           /files/hostname.tmpl
//                    /master/units/kubelet.tmpl
//                           /files/hostname.tmpl
//       templates/aws/worker/units/kubelet-dropin.conf.tmpl
//
func generateMachineConfigs(config *renderConfig, templateDir string) ([]*mcfgv1.MachineConfig, error) {
	platformDirs := []string{}
	if config.Platform == "" {
		return nil, fmt.Errorf("cannot generateMachineConfigs with an empty Platform")
	}

	if config.Platform == "_base" {
		return nil, fmt.Errorf("platform _base unsupported")
	}

	for _, dir := range []string{"_base", config.Platform} {
		path := filepath.Join(templateDir, dir)
		exists, err := existsDir(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			glog.Errorf("could not find expected template directory %s", path)
			return nil, fmt.Errorf("platform %s unsupported", config.Platform)
		}
		platformDirs = append(platformDirs, path)
	}

	return generateMachineConfigsForPlatform(config, platformDirs)
}

// generateMachineConfigsForPlatform generates the MachineConfig for every defined role for a given
// platform. It will merge multiple platforms.
func generateMachineConfigsForPlatform(config *renderConfig, platformDirs []string) ([]*mcfgv1.MachineConfig, error) {
	var configs []*mcfgv1.MachineConfig

	// map from role name to ordered template dirs
	roles := map[string][]string{}

	// platform dirs to get role -> templatedir mapping
	for _, p := range platformDirs {
		infos, err := ioutil.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("failed to read dir %q: %v", p, err)
		}

		for _, info := range infos {
			if !info.IsDir() {
				glog.Infof("ignoring non-directory path %q", info.Name())
				continue
			}
			name := info.Name()
			roles[name] = append(roles[name], filepath.Join(p, name))
		}
	}

	for roleName, roleDirs := range roles {
		cfg, err := generateMachineConfigForRole(config, roleName, roleDirs)
		if err != nil {
			return nil, fmt.Errorf("failed to generate MachineConfig for role %q: %v", roleName, err)
		}
		configs = append(configs, cfg)
	}

	return configs, nil
}

// generateMachineConfigForRole generates the MachineConfig for a single role.
// The directory structure of each 'roleDir' looks like this:
//       worker/units/kubelet.conf.tmpl
//             /files/hostname.tmpl
// Later roleDirs can override entries from earlier roleDirs if the have the same filename
// Zero-length files (empty files) indicate the file should not be created
func generateMachineConfigForRole(config *renderConfig, roleName string, roleDirs []string) (*mcfgv1.MachineConfig, error) {
	files := map[string]string{}
	units := map[string]string{}

	// walk all role dirs, with later ones taking precedence
	for _, roleDir := range roleDirs {
		// magic param
		var walkDest *map[string]string

		walkFn := func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			// empty templates signify don't create
			if info.Size() == 0 {
				delete(*walkDest, info.Name())
				return nil
			}

			filedata, err := ioutil.ReadFile(path)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %v", path, err)
			}

			// Render the template file
			renderedData, err := renderTemplate(*config, path, filedata)
			if err != nil {
				return err
			}
			(*walkDest)[info.Name()] = string(renderedData)
			return nil
		}

		walkDest = &files
		p := filepath.Join(roleDir, filesDir)
		exists, err := existsDir(p)
		if err != nil {
			return nil, err
		}
		if exists {
			if err := filepath.Walk(p, walkFn); err != nil {
				return nil, err
			}
		}

		walkDest = &units
		p = filepath.Join(roleDir, unitsDir)
		exists, err = existsDir(p)
		if err != nil {
			return nil, err
		}
		if exists {
			if err := filepath.Walk(p, walkFn); err != nil {
				return nil, err
			}
		}
	}

	// keySortV returns a list of values, sorted by key
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

	return machineConfigFromIgnConfig(roleName, ignCfg), nil
}

const (
	machineConfigNameTmpl     = "00-%s"
	machineConfigRoleLabelKey = "machineconfiguration.openshift.io/role"

	// DefaultOSImageURL is the value used for OSImageURL field.
	// TODO: this might have to be configured using ControllerConfig.
	DefaultOSImageURL = "://dummy"
)

func machineConfigFromIgnConfig(role string, ignCfg *ignv2_2types.Config) *mcfgv1.MachineConfig {
	name := fmt.Sprintf(machineConfigNameTmpl, role)
	labels := map[string]string{
		machineConfigRoleLabelKey: role,
	}
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Name:   name,
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: DefaultOSImageURL,
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

// renderTemplate renders a template file with values from a renderConfig
// returns the rendered file data
func renderTemplate(config renderConfig, path string, b []byte) ([]byte, error) {

	funcs := sprig.TxtFuncMap()
	funcs["skip"] = skipMissing
	funcs["etcdServerCertDNSNames"] = etcdServerCertDNSNames
	funcs["etcdPeerCertDNSNames"] = etcdPeerCertDNSNames
	funcs["etcdInitialCluster"] = etcdInitialCluster
	funcs["apiServerURL"] = apiServerURL
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
func etcdServerCertDNSNames(cfg renderConfig) (interface{}, error) {
	if cfg.ClusterName == "" || cfg.BaseDomain == "" || cfg.EtcdInitialCount <= 0 {
		return nil, fmt.Errorf("invalid configuration")
	}

	var dnsNames = []string{
		"localhost",
		"*.kube-etcd.kube-system.svc.cluster.local",
		"kube-etcd-client.kube-system.svc.cluster.local",
		"etcd.kube-system.svc",               // sign for the local etcd service name that cluster-network apiservers use to communicate
		"etcd.kube-system.svc.cluster.local", // sign for the local etcd service name that cluster-network apiservers use to communicate
	}

	for i := 0; i < cfg.EtcdInitialCount; i++ {
		dnsNames = append(dnsNames, fmt.Sprintf("%s-etcd-%d.%s", cfg.ClusterName, i, cfg.BaseDomain))
	}
	return strings.Join(dnsNames, ","), nil
}

func etcdPeerCertDNSNames(cfg renderConfig) (interface{}, error) {
	if cfg.ClusterName == "" || cfg.BaseDomain == "" || cfg.EtcdInitialCount <= 0 {
		return nil, fmt.Errorf("invalid configuration")
	}

	var dnsNames = []string{
		"*.kube-etcd.kube-system.svc.cluster.local",
		"kube-etcd-client.kube-system.svc.cluster.local",
	}

	for i := 0; i < cfg.EtcdInitialCount; i++ {
		dnsNames = append(dnsNames, fmt.Sprintf("%s-etcd-%d.%s", cfg.ClusterName, i, cfg.BaseDomain))
	}
	return strings.Join(dnsNames, ","), nil
}

func etcdInitialCluster(cfg renderConfig) (interface{}, error) {
	if cfg.ClusterName == "" || cfg.BaseDomain == "" || cfg.EtcdInitialCount <= 0 {
		return nil, fmt.Errorf("invalid configuration")
	}

	var addresses []string
	for i := 0; i < cfg.EtcdInitialCount; i++ {
		endpoint := fmt.Sprintf("%s-etcd-%d.%s", cfg.ClusterName, i, cfg.BaseDomain)
		addresses = append(addresses, fmt.Sprintf("%s=https://%s:2380", endpoint, endpoint))
	}
	return strings.Join(addresses, ","), nil
}

// generate apiserver url using cluster-name, basename
func apiServerURL(cfg renderConfig) (interface{}, error) {
	if cfg.ClusterName == "" || cfg.BaseDomain == "" {
		return nil, fmt.Errorf("invalid configuration")
	}
	return fmt.Sprintf("https://%s-api.%s:6443", cfg.ClusterName, cfg.BaseDomain), nil
}

func cloudProvider(cfg renderConfig) (interface{}, error) {
	switch cfg.Platform {
	case "aws":
		return "aws", nil
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
