package helpers

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MachineOSConfigBuilder struct {
	mosc *mcfgv1.MachineOSConfig
}

func NewMachineOSConfigBuilder(name string) *MachineOSConfigBuilder {
	return &MachineOSConfigBuilder{
		mosc: &mcfgv1.MachineOSConfig{
			TypeMeta: metav1.TypeMeta{
				Kind:       "MachineOSConfig",
				APIVersion: "machineconfiguration.openshift.io/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      map[string]string{},
				Annotations: map[string]string{},
			},
			Spec: mcfgv1.MachineOSConfigSpec{
				MachineConfigPool: mcfgv1.MachineConfigPoolReference{},

				Containerfile: []mcfgv1.MachineOSContainerfile{},
				ImageBuilder: mcfgv1.MachineOSImageBuilder{
					ImageBuilderType: mcfgv1.JobBuilder,
				},
				BaseImagePullSecret:     nil,
				RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{},
			},
		},
	}
}

func (m *MachineOSConfigBuilder) WithMachineConfigPool(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.MachineConfigPool.Name = name
	return m
}

func (m *MachineOSConfigBuilder) WithBaseImagePullSecret(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.BaseImagePullSecret.Name = name
	return m
}

func (m *MachineOSConfigBuilder) WithFinalImagePushSecret(name string) *MachineOSConfigBuilder {
	return m.WithRenderedImagePushSpec(name)
}

func (m *MachineOSConfigBuilder) WithContainerfile(arch mcfgv1.ContainerfileArch, content string) *MachineOSConfigBuilder {
	m.mosc.Spec.Containerfile = append(m.mosc.Spec.Containerfile, mcfgv1.MachineOSContainerfile{
		ContainerfileArch: arch,
		Content:           content,
	})
	return m
}

func (m *MachineOSConfigBuilder) WithRenderedImagePushSpec(pushspec string) *MachineOSConfigBuilder {
	m.mosc.Spec.RenderedImagePushSpec = mcfgv1.ImageTagFormat(pushspec)
	return m
}

func (m *MachineOSConfigBuilder) WithRenderedImagePushSecret(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.RenderedImagePushSecret.Name = name
	return m
}

func (m *MachineOSConfigBuilder) WithCurrentImagePullspec(pullspec string) *MachineOSConfigBuilder {
	m.mosc.Status.CurrentImagePullSpec = mcfgv1.ImageDigestFormat(pullspec)
	return m
}

func (m *MachineOSConfigBuilder) WithAnnotations(annos map[string]string) *MachineOSConfigBuilder {
	for k, v := range annos {
		m.mosc.Annotations[k] = v
	}

	return m
}

func (m *MachineOSConfigBuilder) WithLabels(labels map[string]string) *MachineOSConfigBuilder {
	for k, v := range labels {
		m.mosc.Labels[k] = v
	}

	return m
}

func (m *MachineOSConfigBuilder) MachineOSConfig() *mcfgv1.MachineOSConfig {
	return m.mosc.DeepCopy()
}
