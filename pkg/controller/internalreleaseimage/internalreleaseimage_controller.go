package internalreleaseimage

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func generateInternalReleaseImageMachineConfig(controllerConfig *mcfgv1.ControllerConfig) ([]*mcfgv1.MachineConfig, error) {
	ignCfg, err := ctrlcommon.TranspileCoreOSConfigToIgn(nil, []string{iriRegistryServiceTemplate})
	if err != nil {
		return nil, fmt.Errorf("error transpiling CoreOS config to Ignition config: %w", err)
	}

	mcfg, err := ctrlcommon.MachineConfigFromIgnConfig("master", "02-internalreleaseimage", ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating MachineConfig from Ignition config: %w", err)
	}

	cref := metav1.NewControllerRef(controllerConfig, mcfgv1.SchemeGroupVersion.WithKind("ControllerConfig"))
	mcfg.SetOwnerReferences([]metav1.OwnerReference{*cref})
	mcfg.SetAnnotations(map[string]string{
		ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
	})

	return []*mcfgv1.MachineConfig{mcfg}, nil
}
