package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	imagev1 "github.com/openshift/api/image/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	"github.com/containers/image/v5/docker/reference"
	"github.com/opencontainers/go-digest"
	apicfgv1 "github.com/openshift/api/config/v1"
	apicfgv1alpha1 "github.com/openshift/api/config/v1alpha1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	buildconstants "github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
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
	apioperatorsv1alpha1.Install(scheme)
	apicfgv1.Install(scheme)
	apicfgv1alpha1.Install(scheme)
	corev1.AddToScheme(scheme)
	codecFactory := serializer.NewCodecFactory(scheme)
	decoder := codecFactory.UniversalDecoder(mcfgv1.GroupVersion, apioperatorsv1alpha1.GroupVersion, apicfgv1.GroupVersion, apicfgv1alpha1.GroupVersion, corev1.SchemeGroupVersion)

	var (
		cconfig              *mcfgv1.ControllerConfig
		featureGate          *apicfgv1.FeatureGate
		nodeConfig           *apicfgv1.Node
		kconfigs             []*mcfgv1.KubeletConfig
		pools                []*mcfgv1.MachineConfigPool
		configs              []*mcfgv1.MachineConfig
		machineOSConfigs     []*mcfgv1.MachineOSConfig
		crconfigs            []*mcfgv1.ContainerRuntimeConfig
		icspRules            []*apioperatorsv1alpha1.ImageContentSourcePolicy
		idmsRules            []*apicfgv1.ImageDigestMirrorSet
		itmsRules            []*apicfgv1.ImageTagMirrorSet
		clusterImagePolicies []*apicfgv1.ClusterImagePolicy
		imagePolicies        []*apicfgv1.ImagePolicy
		imgCfg               *apicfgv1.Image
		apiServer            *apicfgv1.APIServer
		imageStream          *imagev1.ImageStream
	)
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
					klog.V(4).Infof("skipping path %q [%d] manifest because it is not part of expected api group: %v", file.Name(), idx+1, err)
					continue
				}
				return fmt.Errorf("error parsing %q [%d] manifest: %w", file.Name(), idx+1, err)
			}

			switch obj := obji.(type) {
			case *mcfgv1.MachineConfigPool:
				pools = append(pools, obj)
			case *mcfgv1.MachineConfig:
				configs = append(configs, obj)
			case *mcfgv1.MachineOSConfig:
				machineOSConfigs = append(machineOSConfigs, obj)
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
			case *apicfgv1.ClusterImagePolicy:
				clusterImagePolicies = append(clusterImagePolicies, obj)
			case *apicfgv1.ImagePolicy:
				imagePolicies = append(imagePolicies, obj)
			case *apicfgv1.FeatureGate:
				if obj.GetName() == ctrlcommon.ClusterFeatureInstanceName {
					featureGate = obj
				}
			case *apicfgv1.Node:
				if obj.GetName() == ctrlcommon.ClusterNodeInstanceName {
					nodeConfig = obj
				}
			case *apicfgv1.APIServer:
				if obj.GetName() == ctrlcommon.APIServerInstanceName {
					apiServer = obj
				}
			case *imagev1.ImageStream:
				for _, tag := range obj.Spec.Tags {
					if tag.Name == "machine-config-operator" {
						if imageStream != nil {
							klog.Infof("multiple ImageStream found. Previous ImageStream %s replaced by %s", imageStream.Name, obj.Name)
						}
						imageStream = obj

					}
				}
				// It's an ImageStream that doesn't look like the Release one (doesn't have our tag)
			default:
				klog.Infof("skipping %q [%d] manifest because of unhandled %T", file.Name(), idx+1, obji)
			}
		}
	}
	klog.Infof("Bootstrap manifests were successfully processed.")

	if cconfig == nil {
		return fmt.Errorf("error: no controllerconfig found in dir: %q", b.manifestDir)
	}

	if featureGate == nil {
		return fmt.Errorf("error: no featuregate found in dir: %q", b.manifestDir)
	}

	fgHandler, err := ctrlcommon.NewFeatureGatesCRHandlerImpl(featureGate, version.ReleaseVersion)
	if err != nil {
		return fmt.Errorf("error creating feature gates handler: %w", err)
	}

	iconfigs, err := template.RunBootstrap(b.templatesDir, cconfig, psraw, apiServer)
	if err != nil {
		return err
	}
	klog.Infof("Successfully generated MachineConfigs from templates.")

	configs = append(configs, iconfigs...)

	rconfigs, err := containerruntimeconfig.RunImageBootstrap(b.templatesDir, cconfig, pools, icspRules, idmsRules, itmsRules, imgCfg, clusterImagePolicies, imagePolicies, fgHandler)
	if err != nil {
		return err
	}
	klog.Infof("Successfully generated MachineConfigs from image configs.")

	configs = append(configs, rconfigs...)

	if len(crconfigs) > 0 {
		containerRuntimeConfigs, err := containerruntimeconfig.RunContainerRuntimeBootstrap(b.templatesDir, crconfigs, cconfig, pools)
		if err != nil {
			return err
		}
		configs = append(configs, containerRuntimeConfigs...)
	}
	klog.Infof("Successfully generated MachineConfigs from containerruntime.")

	if featureGate != nil {
		featureConfigs, err := kubeletconfig.RunFeatureGateBootstrap(b.templatesDir, fgHandler, nodeConfig, cconfig, pools, apiServer)
		if err != nil {
			return err
		}
		configs = append(configs, featureConfigs...)
	}
	klog.Infof("Successfully generated MachineConfigs from feature gates.")

	if nodeConfig == nil {
		nodeConfig = &apicfgv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: ctrlcommon.ClusterNodeInstanceName,
			},
			Spec: apicfgv1.NodeSpec{},
		}
	}
	if nodeConfig != nil {
		nodeConfigs, err := kubeletconfig.RunNodeConfigBootstrap(b.templatesDir, fgHandler, cconfig, nodeConfig, pools, apiServer)
		if err != nil {
			return err
		}
		configs = append(configs, nodeConfigs...)
	}
	klog.Infof("Successfully generated MachineConfigs from node.Configs.")

	if len(kconfigs) > 0 {
		kconfigs, err := kubeletconfig.RunKubeletBootstrap(b.templatesDir, kconfigs, cconfig, fgHandler, nodeConfig, pools, apiServer)
		if err != nil {
			return err
		}
		configs = append(configs, kconfigs...)
	}
	klog.Infof("Successfully generated MachineConfigs from kubelet configs.")

	// Create component MachineConfigs for pre-built images for hybrid OCL
	// This must happen BEFORE render.RunBootstrap() so they can be merged into rendered MCs
	if len(machineOSConfigs) > 0 {
		klog.Infoln("Found machineOSConfig(s) at install time, will install cluster with Image Mode enabled")
		preBuiltImageMCs, err := createPreBuiltImageMachineConfigs(machineOSConfigs, pools)
		if err != nil {
			return fmt.Errorf("failed to create pre-built image MachineConfigs: %w", err)
		}
		configs = append(configs, preBuiltImageMCs...)
		klog.Infof("Successfully created %d pre-built image component MachineConfigs for hybrid OCL.", len(preBuiltImageMCs))
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

	// If an apiServer object exists, write it to /etc/mcs/bootstrap/api-server/api-server.yaml
	// so that bootstrap MCS can consume it
	if apiServer != nil {
		buf := bytes.Buffer{}
		encoder := codecFactory.EncoderForVersion(serializer, apicfgv1.GroupVersion)
		err = encoder.Encode(apiServer, &buf)
		if err != nil {
			return err
		}
		apiserverDir := filepath.Join(destDir, "api-server")
		if err := os.MkdirAll(apiserverDir, 0o764); err != nil {
			return err
		}
		// Disable gosec here to avoid throwing
		// G306: Expect WriteFile permissions to be 0600 or less
		// #nosec
		klog.Infof("writing the following apiserver object to disk: %s", string(buf.Bytes()))
		if err := os.WriteFile(filepath.Join(apiserverDir, "api-server.yaml"), buf.Bytes(), 0o664); err != nil {
			return err
		}
	}

	buf := bytes.Buffer{}
	err = encoder.Encode(cconfig, &buf)
	if err != nil {
		return err
	}
	cconfigDir := filepath.Join(destDir, "controller-config")
	if err := os.MkdirAll(cconfigDir, 0o764); err != nil {
		return err
	}

	if imageStream != nil {
		klog.Infof("ImageStream found!")
	}

	klog.Infof("writing the following controllerConfig to disk: %s", string(buf.Bytes()))
	return os.WriteFile(filepath.Join(cconfigDir, "machine-config-controller.yaml"), buf.Bytes(), 0o664)

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

// createPreBuiltImageMachineConfigs creates component MachineConfigs that set osImageURL for pools
// that have associated MachineOSConfigs with pre-built image annotations.
// These component MCs will be automatically merged into rendered MCs by the render controller.
// This function validates pre-built images at bootstrap time and will fail if:
// - The annotation is missing for a MOSC that hasn't been seeded yet (required for install-time OCL)
// - The image format is invalid
// MOSCs without the annotation are skipped only if seeding has already completed.
func createPreBuiltImageMachineConfigs(machineOSConfigs []*mcfgv1.MachineOSConfig, pools []*mcfgv1.MachineConfigPool) ([]*mcfgv1.MachineConfig, error) {
	var preBuiltImageMCs []*mcfgv1.MachineConfig
	var validationErrors []error

	for _, mosc := range machineOSConfigs {
		preBuiltImage, hasPreBuiltImage := mosc.Annotations[buildconstants.PreBuiltImageAnnotationKey]

		// Check if seeding has completed (either Seeded condition or currentBuild annotation)
		hasSeededCondition := apihelpers.IsMachineOSConfigConditionPresentAndEqual(mosc.Status.Conditions, buildconstants.MachineOSConfigSeeded, metav1.ConditionTrue)
		hasCurrentBuild := mosc.Annotations[buildconstants.CurrentMachineOSBuildAnnotationKey] != ""
		isPostSeeding := hasSeededCondition || hasCurrentBuild

		// Validate annotation presence
		if !hasPreBuiltImage || preBuiltImage == "" {
			if isPostSeeding {
				// Post-seeding: annotation was removed after seeding completed, this is OK, skip
				klog.V(4).Infof("Skipping MachineOSConfig %s - no pre-built image annotation but seeding already completed", mosc.Name)
				continue
			}
			// Pre-seeding: annotation is REQUIRED for install-time OCL
			validationErrors = append(validationErrors, fmt.Errorf("MachineOSConfig %q is missing required annotation %q - this is required for install-time OCL",
				mosc.Name, buildconstants.PreBuiltImageAnnotationKey))
			continue
		}

		poolName := mosc.Spec.MachineConfigPool.Name
		// verify that the pool used in the MOSC actually exists
		poolExists := false
		for _, pool := range pools {
			if pool.Name == poolName {
				poolExists = true
				break
			}
		}
		if !poolExists {
			validationErrors = append(validationErrors, fmt.Errorf("MachineOSConfig %q references non-existent pool %q", mosc.Name, poolName))
			continue
		}

		// Validate the pre-built image format and digest
		if err := validatePreBuiltImage(preBuiltImage); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("invalid pre-built image %q for MachineOSConfig %q (pool %q): %w",
				preBuiltImage, mosc.Name, poolName, err))
			continue
		}

		// Create the component MachineConfig
		mc := ctrlcommon.CreatePreBuiltImageMachineConfig(poolName, preBuiltImage, buildconstants.PreBuiltImageAnnotationKey)
		preBuiltImageMCs = append(preBuiltImageMCs, mc)
		klog.Infof("Validated and created component MachineConfig %s with OSImageURL: %s for pool %s", mc.Name, preBuiltImage, poolName)
	}

	// Return all validation errors if any occurred
	if len(validationErrors) > 0 {
		return nil, fmt.Errorf("validation failed for MachineOSConfigs: %s", errors.Join(validationErrors...))
	}

	return preBuiltImageMCs, nil
}

// validatePreBuiltImage validates the pre-built image format using containers/image library
func validatePreBuiltImage(imageSpec string) error {
	if imageSpec == "" {
		return fmt.Errorf("pre-built image spec cannot be empty")
	}

	// Use the containers/image library to parse and validate the image reference
	ref, err := reference.ParseNamed(imageSpec)
	if err != nil {
		return fmt.Errorf("pre-built image %q has invalid format: %w", imageSpec, err)
	}

	// Ensure the reference has a digest (is canonical)
	canonical, ok := ref.(reference.Canonical)
	if !ok {
		return fmt.Errorf("pre-built image must use digested format (image@sha256:digest), got: %q", imageSpec)
	}

	// Validate the digest using the go-digest library
	if err := canonical.Digest().Validate(); err != nil {
		return fmt.Errorf("pre-built image %q has invalid digest: %w", imageSpec, err)
	}

	// Ensure it's specifically a SHA256 digest (which is what we expect for container images)
	if canonical.Digest().Algorithm() != digest.SHA256 {
		return fmt.Errorf("pre-built image must use SHA256 digest, got %s: %q", canonical.Digest().Algorithm(), imageSpec)
	}

	return nil
}
