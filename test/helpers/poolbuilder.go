package helpers

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// MachineConfigPoolBuilder provides a more fluent API for creating
// MachineConfigPool objects for tests. Instead of creating multiple specific
// functions for each particular node configuration, this allows one to create
// nodes thusly:
//
// NewMachineConfigPoolBuilder("worker").WithMachineConfig(currentConfig).MachineConfigPool()
//
// It is aware of the rules around creating layering-enabled
// MachineConfigPools. For example, if one sets an image with the WithImage()
// method, it will automatically set the layering annotation.
type MachineConfigPoolBuilder struct {
	name           string
	currentConfig  string
	annos          map[string]string
	labels         map[string]string
	mcSelector     *metav1.LabelSelector
	nodeSelector   *metav1.LabelSelector
	conditions     []*mcfgv1.MachineConfigPoolCondition
	maxUnavailable *intstr.IntOrString
	paused         bool
	childConfigs   []corev1.ObjectReference
}

func NewMachineConfigPoolBuilder(name string) *MachineConfigPoolBuilder {
	return &MachineConfigPoolBuilder{name: name}
}

func (m *MachineConfigPoolBuilder) WithName(name string) *MachineConfigPoolBuilder {
	m.name = name
	return m
}

func (m *MachineConfigPoolBuilder) WithPaused() *MachineConfigPoolBuilder {
	m.paused = true
	return m
}

func (m *MachineConfigPoolBuilder) WithMaxUnavailable(n int) *MachineConfigPoolBuilder {
	// TODO: Update k8s.io/apimachinery since this now uses an int32 instead of an int.
	tmp := intstr.FromInt(n)
	m.maxUnavailable = &tmp
	return m
}

func (m *MachineConfigPoolBuilder) WithMachineConfig(mc string) *MachineConfigPoolBuilder {
	m.currentConfig = mc
	return m
}

func (m *MachineConfigPoolBuilder) WithAnnotations(annos map[string]string) *MachineConfigPoolBuilder {
	if m.annos == nil {
		m.annos = map[string]string{}
	}

	for k, v := range annos {
		m.annos[k] = v
	}

	return m
}

func (m *MachineConfigPoolBuilder) WithLabels(labels map[string]string) *MachineConfigPoolBuilder {
	if m.labels == nil {
		m.labels = map[string]string{}
	}

	for k, v := range labels {
		m.labels[k] = v
	}

	return m
}

func (m *MachineConfigPoolBuilder) WithChildConfigs(names []string) *MachineConfigPoolBuilder {
	if m.childConfigs == nil {
		m.childConfigs = []corev1.ObjectReference{}
	}

	for _, name := range names {
		m.childConfigs = append(m.childConfigs, corev1.ObjectReference{
			Name: name,
			Kind: "MachineConfig",
		})
	}

	return m
}

func (m *MachineConfigPoolBuilder) WithNodeSelector(ns *metav1.LabelSelector) *MachineConfigPoolBuilder {
	m.nodeSelector = ns
	return m
}

func (m *MachineConfigPoolBuilder) WithMachineConfigSelector(mcs *metav1.LabelSelector) *MachineConfigPoolBuilder {
	m.mcSelector = mcs
	return m
}

func (m *MachineConfigPoolBuilder) MachineConfigPool() *mcfgv1.MachineConfigPool {
	mcp := NewMachineConfigPool(m.name, m.mcSelector, m.nodeSelector, m.currentConfig)

	mcp.Spec.Paused = m.paused
	mcp.Spec.MaxUnavailable = m.maxUnavailable

	if m.annos != nil {
		if mcp.Annotations == nil {
			mcp.Annotations = map[string]string{}
		}

		for k, v := range m.annos {
			mcp.Annotations[k] = v
		}
	}

	if m.labels != nil {
		if mcp.Labels == nil {
			mcp.Labels = map[string]string{}
		}

		for k, v := range m.labels {
			mcp.Labels[k] = v
		}
	}

	if m.conditions != nil {
		for _, condition := range m.conditions {
			apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *condition)
		}
	}

	if m.childConfigs != nil {
		mcp.Spec.Configuration.Source = m.childConfigs
		mcp.Status.Configuration.Source = m.childConfigs
	}

	return mcp
}
