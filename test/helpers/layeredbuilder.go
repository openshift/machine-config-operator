package helpers

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
)

type LayeredBuilder struct {
	mosbBuilder *MachineOSBuildBuilder
	moscBuilder *MachineOSConfigBuilder
	mcpBuilder  *MachineConfigPoolBuilder
}

func NewLayeredBuilder(poolName string) *LayeredBuilder {
	mcpBuilder := NewMachineConfigPoolBuilder(poolName)

	moscBuilder := NewMachineOSConfigBuilder(poolName).
		WithMachineConfigPool(poolName)

	mosbBuilder := NewMachineOSBuildBuilder(poolName).
		WithMachineOSConfig(poolName)

	return &LayeredBuilder{
		mosbBuilder: mosbBuilder,
		moscBuilder: moscBuilder,
		mcpBuilder:  mcpBuilder,
	}
}

func (l *LayeredBuilder) WithDesiredConfig(name string) *LayeredBuilder {
	l.mcpBuilder.WithMachineConfig(name)
	l.mosbBuilder.WithDesiredConfig(name)
	l.mosbBuilder.mosb.Name = fmt.Sprintf("%s-%s-builder", l.mcpBuilder.name, l.mcpBuilder.currentConfig)
	return l
}

func (l *LayeredBuilder) MachineConfigPoolBuilder() *MachineConfigPoolBuilder {
	return l.mcpBuilder
}

func (l *LayeredBuilder) MachineOSConfigBuilder() *MachineOSConfigBuilder {
	return l.moscBuilder
}

func (l *LayeredBuilder) MachineOSBuildBuilder() *MachineOSBuildBuilder {
	return l.mosbBuilder
}

func (l *LayeredBuilder) MachineConfigPool() *mcfgv1.MachineConfigPool {
	return l.mcpBuilder.MachineConfigPool()
}

func (l *LayeredBuilder) MachineOSConfig() *mcfgv1alpha1.MachineOSConfig {
	return l.moscBuilder.MachineOSConfig()
}

func (l *LayeredBuilder) MachineOSBuild() *mcfgv1alpha1.MachineOSBuild {
	if l.mcpBuilder.currentConfig == "" {
		l.WithDesiredConfig(fmt.Sprintf("rendered-%s", l.mcpBuilder.name))
	} else {
		l.WithDesiredConfig(l.mcpBuilder.currentConfig)
	}

	return l.mosbBuilder.MachineOSBuild()
}
