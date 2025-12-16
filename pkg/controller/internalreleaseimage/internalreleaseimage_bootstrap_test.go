package internalreleaseimage

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/stretchr/testify/assert"
)

func TestRunInternalReleaseImageBootstrap(t *testing.T) {
	cc := &mcfgv1.ControllerConfig{
		Spec: mcfgv1.ControllerConfigSpec{
			Images: map[string]string{
				templatectrl.DockerRegistryKey: "docker-registry-image-pullspec",
			},
		},
	}

	mc, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, cc)
	assert.NoError(t, err)
	assert.Equal(t, mc.Name, iriMachineConfigName)
	assert.Equal(t, mc.Labels[mcfgv1.MachineConfigRoleLabelKey], "master")
	assert.Equal(t, mc.OwnerReferences[0].Kind, "InternalReleaseImage")
	assert.Contains(t, string(mc.Spec.Config.Raw), "docker-registry-image-pullspec")
}
