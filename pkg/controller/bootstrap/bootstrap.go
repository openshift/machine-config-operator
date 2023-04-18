package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	kscheme "k8s.io/client-go/kubernetes/scheme"

	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1 "github.com/openshift/api/operator/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
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
// nolint:gocyclo
func (b *Bootstrap) Run(destDir string) error {
	infos, err := ctrlcommon.ReadDir(b.manifestDir)
	if err != nil {
		return err
	}

	psfraw, err := os.ReadFile(b.pullSecretFile)
	if err != nil {
		return err
	}

	psraw, err := getPullSecretFromSecret(psfraw)
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	mcfgv1.Install(scheme)
	apioperatorsv1.Install(scheme)
	apioperatorsv1alpha1.Install(scheme)
	apicfgv1.Install(scheme)
	codecFactory := serializer.NewCodecFactory(scheme)
	decoder := codecFactory.UniversalDecoder(mcfgv1.GroupVersion, apioperatorsv1.GroupVersion, apioperatorsv1alpha1.GroupVersion, apicfgv1.GroupVersion)

	var cconfig *mcfgv1.ControllerConfig
	var featureGate *apicfgv1.FeatureGate
	var nodeConfig *apicfgv1.Node
	var storageConfig *apioperatorsv1.Storage
	var kconfigs []*mcfgv1.KubeletConfig
	var pools []*mcfgv1.MachineConfigPool
	var configs []*mcfgv1.MachineConfig
	var crconfigs []*mcfgv1.ContainerRuntimeConfig
	var icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy
	var idmsRules []*apicfgv1.ImageDigestMirrorSet
	var itmsRules []*apicfgv1.ImageTagMirrorSet
	var imgCfg *apicfgv1.Image
	for _, info := range infos {
		if info.IsDir() {
			continue
		}

		file, err := os.Open(filepath.Join(b.manifestDir, info.Name()))
		if err != nil {
			return fmt.Errorf("error opening %s: %w", file.Name(), err)
		}
		defer file.Close()

		manifests, err := parseManifests(file.Name(), file)
		if err != nil {
			return fmt.Errorf("error parsing manifests from %s: %w", file.Name(), err)
		}

		for idx, m := range manifests {
			obji, err := runtime.Decode(decoder, m.Raw)
			if err != nil {
				if runtime.IsNotRegisteredError(err) {
					// don't care
					glog.V(4).Infof("skipping path %q [%d] manifest because it is not part of expected api group: %v", file.Name(), idx+1, err)
					continue
				}
				return fmt.Errorf("error parsing %q [%d] manifest: %w", file.Name(), idx+1, err)
			}

			switch obj := obji.(type) {
			case *mcfgv1.MachineConfigPool:
				pools = append(pools, obj)
			case *mcfgv1.MachineConfig:
				configs = append(configs, obj)
			case *mcfgv1.ControllerConfig:
				cconfig = obj
			case *mcfgv1.ContainerRuntimeConfig:
				crconfigs = append(crconfigs, obj)
			case *mcfgv1.KubeletConfig:
				kconfigs = append(kconfigs, obj)
			case *apioperatorsv1alpha1.ImageContentSourcePolicy:
				icspRules = append(icspRules, obj)
			case *apicfgv1.ImageDigestMirrorSet:
				idmsRules = append(idmsRules, obj)
			case *apicfgv1.ImageTagMirrorSet:
				itmsRules = append(itmsRules, obj)
			case *apicfgv1.Image:
				imgCfg = obj
			case *apicfgv1.FeatureGate:
				if obj.GetName() == ctrlcommon.ClusterFeatureInstanceName {
					featureGate = obj
				}
			case *apicfgv1.Node:
				if obj.GetName() == ctrlcommon.ClusterNodeInstanceName {
					nodeConfig = obj
				}
			case *apioperatorsv1.Storage:
				if obj.GetName() == ctrlcommon.ClusterStorageInstanceName {
					storageConfig = obj
				}
			default:
				glog.Infof("skipping %q [%d] manifest because of unhandled %T", file.Name(), idx+1, obji)
			}
		}
	}

	if cconfig == nil {
		return fmt.Errorf("error: no controllerconfig found in dir: %q", destDir)
	}
	iconfigs, err := template.RunBootstrap(b.templatesDir, cconfig, psraw, featureGate, storageConfig)
	if err != nil {
		return err
	}
	configs = append(configs, iconfigs...)

	rconfigs, err := containerruntimeconfig.RunImageBootstrap(b.templatesDir, cconfig, pools, icspRules, idmsRules, itmsRules, imgCfg)
	if err != nil {
		return err
	}

	configs = append(configs, rconfigs...)

	if len(crconfigs) > 0 {
		containerRuntimeConfigs, err := containerruntimeconfig.RunContainerRuntimeBootstrap(b.templatesDir, crconfigs, cconfig, pools)
		if err != nil {
			return err
		}
		configs = append(configs, containerRuntimeConfigs...)
	}
	if featureGate != nil {
		featureConfigs, err := kubeletconfig.RunFeatureGateBootstrap(b.templatesDir, featureGate, nodeConfig, cconfig, pools)
		if err != nil {
			return err
		}
		configs = append(configs, featureConfigs...)
	}

	if nodeConfig == nil {
		nodeConfig = &apicfgv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: ctrlcommon.ClusterNodeInstanceName,
			},
			Spec: apicfgv1.NodeSpec{},
		}
	}
	if nodeConfig != nil {
		nodeConfigs, err := kubeletconfig.RunNodeConfigBootstrap(b.templatesDir, featureGate, cconfig, nodeConfig, pools)
		if err != nil {
			return err
		}
		configs = append(configs, nodeConfigs...)
	}
	if len(kconfigs) > 0 {
		kconfigs, err := kubeletconfig.RunKubeletBootstrap(b.templatesDir, kconfigs, cconfig, featureGate, nodeConfig, pools)
		if err != nil {
			return err
		}
		configs = append(configs, kconfigs...)
	}

	fpools, gconfigs, err := render.RunBootstrap(pools, configs, cconfig)
	if err != nil {
		return err
	}

	serializer := json.NewYAMLSerializer(json.DefaultMetaFactory, scheme, scheme)
	encoder := codecFactory.EncoderForVersion(serializer, mcfgv1.GroupVersion)

	poolsdir := filepath.Join(destDir, "machine-pools")
	if err := os.MkdirAll(poolsdir, 0o764); err != nil {
		return err
	}
	for _, p := range fpools {
		buf := bytes.Buffer{}
		err := encoder.Encode(p, &buf)
		if err != nil {
			return err
		}
		path := filepath.Join(poolsdir, fmt.Sprintf("%s.yaml", p.Name))
		// Disable gosec here to avoid throwing
		// G306: Expect WriteFile permissions to be 0600 or less
		// #nosec
		if err := os.WriteFile(path, buf.Bytes(), 0o664); err != nil {
			return err
		}
	}

	configdir := filepath.Join(destDir, "machine-configs")
	if err := os.MkdirAll(configdir, 0o764); err != nil {
		return err
	}
	for _, c := range gconfigs {
		buf := bytes.Buffer{}
		err := encoder.Encode(c, &buf)
		if err != nil {
			return err
		}
		path := filepath.Join(configdir, fmt.Sprintf("%s.yaml", c.Name))
		// Disable gosec here to avoid throwing
		// G306: Expect WriteFile permissions to be 0600 or less
		// #nosec
		if err := os.WriteFile(path, buf.Bytes(), 0o664); err != nil {
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
			return manifests, fmt.Errorf("error parsing %q: %w", filename, err)
		}
		m.Raw = bytes.TrimSpace(m.Raw)
		if len(m.Raw) == 0 || bytes.Equal(m.Raw, []byte("null")) {
			continue
		}
		manifests = append(manifests, m)
	}
}
