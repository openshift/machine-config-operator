package bootstrap

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
)

// Bootstrap defines boostrap mode for Machine Config Controller
type Bootstrap struct {
	// dir used by template controller to render internal machineconfigs.
	templatesDir string
	// dir used to read pools and user defined machineconfigs.
	manifestDir string
}

// New returns controller for bootstrap
func New(templatesDir, manifestDir string) *Bootstrap {
	return &Bootstrap{
		templatesDir: templatesDir,
		manifestDir:  manifestDir,
	}
}

// Run runs boostrap for Machine Config Controller
// It writes all the assets to destDir
func (b *Bootstrap) Run(destDir string) error {
	infos, err := ioutil.ReadDir(b.manifestDir)
	if err != nil {
		return err
	}

	var cconfig *v1.ControllerConfig
	var pools []*v1.MachineConfigPool
	var configs []*v1.MachineConfig
	for _, info := range infos {
		if info.IsDir() {
			continue
		}

		path := filepath.Join(b.manifestDir, info.Name())
		raw, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(v1.SchemeGroupVersion), raw)
		if err != nil {
			glog.V(4).Infof("skipping path because of error: %v", err)
			// don't care
			continue
		}

		switch obj := obji.(type) {
		case *v1.MachineConfigPool:
			pools = append(pools, obj)
		case *v1.MachineConfig:
			configs = append(configs, obj)
		case *v1.ControllerConfig:
			cconfig = obj
		default:
			glog.Infof("skipping %T", path, obji)
		}
	}

	iconfigs, err := template.RunBootstrap(b.templatesDir, cconfig)
	if err != nil {
		return err
	}
	configs = append(configs, iconfigs...)

	fpools, gconfigs, err := render.RunBootstrap(pools, configs)
	if err != nil {
		return err
	}

	poolsdir := filepath.Join(destDir, "machine-pools")
	if err := os.MkdirAll(poolsdir, 0664); err != nil {
		return err
	}
	for _, p := range fpools {
		b, err := yaml.Marshal(p)
		if err != nil {
			return err
		}
		path := filepath.Join(poolsdir, fmt.Sprintf("%s.yaml", p.Name))
		if err := ioutil.WriteFile(path, b, 0664); err != nil {
			return err
		}
	}

	configdir := filepath.Join(destDir, "machine-configs")
	if err := os.MkdirAll(poolsdir, 0664); err != nil {
		return err
	}
	for _, c := range gconfigs {
		b, err := yaml.Marshal(c)
		if err != nil {
			return err
		}
		path := filepath.Join(configdir, fmt.Sprintf("%s.yaml", c.Name))
		if err := ioutil.WriteFile(path, b, 0664); err != nil {
			return err
		}
	}
	return nil
}
