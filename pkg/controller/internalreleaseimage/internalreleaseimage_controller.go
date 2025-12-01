package internalreleaseimage

import (
	"bytes"
	"text/template"

	"fmt"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	iriRole              = "master"
	iriMachineConfigName = "02-master-internalreleaseimage"
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = mcfgv1alpha1.SchemeGroupVersion.WithKind("InternalReleaseImage")

func generateInternalReleaseImageMachineConfig(iri *mcfgv1alpha1.InternalReleaseImage, controllerConfig *mcfgv1.ControllerConfig) (*mcfgv1.MachineConfig, error) {
	ignCfg, err := generateIgnitionFromTemplate(controllerConfig)
	if err != nil {
		return nil, err
	}

	mcfg, err := ctrlcommon.MachineConfigFromIgnConfig(iriRole, iriMachineConfigName, ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating MachineConfig from Ignition config: %w", err)
	}

	cref := metav1.NewControllerRef(iri, controllerKind)
	mcfg.SetOwnerReferences([]metav1.OwnerReference{*cref})
	mcfg.SetAnnotations(map[string]string{
		ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
	})

	return mcfg, nil
}

func generateIgnitionFromTemplate(controllerConfig *mcfgv1.ControllerConfig) (*ign3types.Config, error) {
	// Parse the iri template
	tmpl, err := template.New("iri-template").Parse(iriRegistryServiceTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse iri-template : %w", err)
	}

	type iriRenderConfig struct {
		DockerRegistryImage string
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, iriRenderConfig{
		DockerRegistryImage: controllerConfig.Spec.Images[templatectrl.DockerRegistryKey],
	}); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	// Generate the iri ignition
	ignCfg, err := ctrlcommon.TranspileCoreOSConfigToIgn(nil, []string{buf.String()})
	if err != nil {
		return nil, fmt.Errorf("error transpiling CoreOS config to Ignition config: %w", err)
	}
	return ignCfg, nil
}
