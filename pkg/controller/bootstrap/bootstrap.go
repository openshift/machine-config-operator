package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	kscheme "k8s.io/client-go/kubernetes/scheme"

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
	// pull secret file
	pullSecretFile string
}

// New returns controller for bootstrap
func New(templatesDir, manifestDir, pullSecretFile string) *Bootstrap {
	return &Bootstrap{
		templatesDir:   templatesDir,
		manifestDir:    manifestDir,
		pullSecretFile: pullSecretFile,
	}
}

// Run runs boostrap for Machine Config Controller
// It writes all the assets to destDir
func (b *Bootstrap) Run(destDir string) error {
	infos, err := ioutil.ReadDir(b.manifestDir)
	if err != nil {
		return err
	}

	psfraw, err := ioutil.ReadFile(b.pullSecretFile)
	if err != nil {
		return err
	}

	psraw, err := getPullSecretFromSecret(psfraw)
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

		file, err := os.Open(filepath.Join(b.manifestDir, info.Name()))
		if err != nil {
			return fmt.Errorf("error opening %s: %v", file.Name(), err)
		}
		defer file.Close()

		manifests, err := parseManifests(file.Name(), file)
		if err != nil {
			return fmt.Errorf("error parsing manifests from %s: %v", file.Name(), err)
		}

		for idx, m := range manifests {
			obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(v1.SchemeGroupVersion), m.Raw)
			if err != nil {
				if runtime.IsNotRegisteredError(err) {
					// don't care
					glog.V(4).Infof("skipping path %q [%d] manifest because it is not part of expected api group: %v", file.Name(), idx+1, err)
					continue
				}
				return fmt.Errorf("error parsing %q [%d] manifest: %v", file.Name(), idx+1, err)
			}

			switch obj := obji.(type) {
			case *v1.MachineConfigPool:
				pools = append(pools, obj)
			case *v1.MachineConfig:
				configs = append(configs, obj)
			case *v1.ControllerConfig:
				cconfig = obj
			default:
				glog.Infof("skipping %q [%d] manifest because of unhandled %T", file.Name(), idx+1, obji)
			}
		}
	}

	if cconfig == nil {
		return fmt.Errorf("error: no controllerconfig found in dir: %q", destDir)
	}
	iconfigs, err := template.RunBootstrap(b.templatesDir, cconfig, psraw)
	if err != nil {
		return err
	}
	configs = append(configs, iconfigs...)

	fpools, gconfigs, err := render.RunBootstrap(pools, configs, cconfig)
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
	if err := os.MkdirAll(configdir, 0664); err != nil {
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

func getPullSecretFromSecret(sData []byte) ([]byte, error) {
	obji, err := runtime.Decode(kscheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), sData)
	if err != nil {
		return nil, err
	}
	s, ok := obji.(*corev1.Secret)
	if !ok {
		return nil, fmt.Errorf("expected *corev1.Secret found %T", obji)
	}
	if s.Type != corev1.SecretTypeDockerConfigJson {
		return nil, fmt.Errorf("expected secret type %s found %s", corev1.SecretTypeDockerConfigJson, s.Type)
	}
	return s.Data[corev1.DockerConfigJsonKey], nil
}

type manifest struct {
	Raw []byte
}

// UnmarshalJSON unmarshals bytes of single kubernetes object to manifest.
func (m *manifest) UnmarshalJSON(in []byte) error {
	if m == nil {
		return errors.New("Manifest: UnmarshalJSON on nil pointer")
	}

	// This happens when marshalling
	// <yaml>
	// ---	(this between two `---`)
	// ---
	// <yaml>
	if bytes.Equal(in, []byte("null")) {
		m.Raw = nil
		return nil
	}

	m.Raw = append(m.Raw[0:0], in...)
	return nil
}

// parseManifests parses a YAML or JSON document that may contain one or more
// kubernetes resources.
func parseManifests(filename string, r io.Reader) ([]manifest, error) {
	d := yamlutil.NewYAMLOrJSONDecoder(r, 1024)
	var manifests []manifest
	for {
		m := manifest{}
		if err := d.Decode(&m); err != nil {
			if err == io.EOF {
				return manifests, nil
			}
			return manifests, fmt.Errorf("error parsing %q: %v", filename, err)
		}
		m.Raw = bytes.TrimSpace(m.Raw)
		if len(m.Raw) == 0 || bytes.Equal(m.Raw, []byte("null")) {
			continue
		}
		manifests = append(manifests, m)
	}
}
