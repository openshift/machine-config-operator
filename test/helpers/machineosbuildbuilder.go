package helpers

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MachineOSBuildBuilder struct {
	mosb *mcfgv1alpha1.MachineOSBuild
}

func NewMachineOSBuildBuilder(name string) *MachineOSBuildBuilder {
	return &MachineOSBuildBuilder{
		mosb: &mcfgv1alpha1.MachineOSBuild{
			TypeMeta: metav1.TypeMeta{
				Kind:       "MachineOSBuild",
				APIVersion: "machineconfiguration.openshift.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				// Name: fmt.Sprintf("%s-%s-builder", config.Spec.MachineConfigPool.Name, pool.Spec.Configuration.Name),
				Name: name,
			},
			Spec: mcfgv1alpha1.MachineOSBuildSpec{
				Version:          1,
				ConfigGeneration: 1,
				DesiredConfig:    mcfgv1alpha1.RenderedMachineConfigReference{},
				MachineOSConfig:  mcfgv1alpha1.MachineOSConfigReference{},
			},
		},
	}
}

func NewMachineOSBuildBuilderFromMachineConfigPool(mcp *mcfgv1.MachineConfigPool) *MachineOSBuildBuilder {
	m := NewMachineOSBuildBuilder(fmt.Sprintf("%s-%s-builder", mcp.Name, mcp.Spec.Configuration.Name))
	m.mosb.Spec.DesiredConfig.Name = mcp.Spec.Configuration.Name
	return m
}

func (m *MachineOSBuildBuilder) WithRenderedImagePushspec(pushspec string) *MachineOSBuildBuilder {
	m.mosb.Spec.RenderedImagePushspec = pushspec
	return m
}

func (m *MachineOSBuildBuilder) WithMachineOSConfig(name string) *MachineOSBuildBuilder {
	m.mosb.Spec.MachineOSConfig.Name = name
	return m
}

func (m *MachineOSBuildBuilder) WithDesiredConfig(name string) *MachineOSBuildBuilder {
	m.mosb.Spec.DesiredConfig.Name = name
	return m
}

func (m *MachineOSBuildBuilder) MachineOSBuild() *mcfgv1alpha1.MachineOSBuild {
	return m.mosb.DeepCopy()
}
