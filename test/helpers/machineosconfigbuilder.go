package helpers

import (
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MachineOSConfigBuilder struct {
	mosc *mcfgv1alpha1.MachineOSConfig
}

func NewMachineOSConfigBuilder(name string) *MachineOSConfigBuilder {
	return &MachineOSConfigBuilder{
		mosc: &mcfgv1alpha1.MachineOSConfig{
			TypeMeta: metav1.TypeMeta{
				Kind:       "MachineOSConfig",
				APIVersion: "machineconfiguration.openshift.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      map[string]string{},
				Annotations: map[string]string{},
			},
			Spec: mcfgv1alpha1.MachineOSConfigSpec{
				MachineConfigPool: mcfgv1alpha1.MachineConfigPoolReference{},
				BuildInputs: mcfgv1alpha1.BuildInputs{
					Containerfile: []mcfgv1alpha1.MachineOSContainerfile{},
					ImageBuilder: &mcfgv1alpha1.MachineOSImageBuilder{
						ImageBuilderType: mcfgv1alpha1.MachineOSImageBuilderType("PodImageBuilder"),
					},
					BaseImagePullSecret:     mcfgv1alpha1.ImageSecretObjectReference{},
					RenderedImagePushSecret: mcfgv1alpha1.ImageSecretObjectReference{},
				},
				BuildOutputs: mcfgv1alpha1.BuildOutputs{
					CurrentImagePullSecret: mcfgv1alpha1.ImageSecretObjectReference{},
				},
			},
		},
	}
}

func (m *MachineOSConfigBuilder) WithReleaseVersion(version string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.ReleaseVersion = version
	return m
}

func (m *MachineOSConfigBuilder) WithBaseOSImagePullspec(pullspec string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.BaseOSImagePullspec = pullspec
	return m
}

func (m *MachineOSConfigBuilder) WithMachineConfigPool(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.MachineConfigPool.Name = name
	return m
}

func (m *MachineOSConfigBuilder) WithBaseImagePullSecret(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.BaseImagePullSecret.Name = name
	return m
}

func (m *MachineOSConfigBuilder) WithFinalImagePushSecret(name string) *MachineOSConfigBuilder {
	return m.WithRenderedImagePushspec(name)
}

func (m *MachineOSConfigBuilder) WithContainerfile(arch mcfgv1alpha1.ContainerfileArch, content string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.Containerfile = append(m.mosc.Spec.BuildInputs.Containerfile, mcfgv1alpha1.MachineOSContainerfile{
		ContainerfileArch: arch,
		Content:           content,
	})
	return m
}

func (m *MachineOSConfigBuilder) WithRenderedImagePushspec(pushspec string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.RenderedImagePushspec = pushspec
	return m
}

func (m *MachineOSConfigBuilder) WithRenderedImagePushSecret(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.RenderedImagePushSecret.Name = name
	return m
}

func (m *MachineOSConfigBuilder) WithExtensionsImagePullspec(pullspec string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildInputs.BaseOSExtensionsImagePullspec = pullspec
	return m
}

func (m *MachineOSConfigBuilder) WithCurrentImagePullspec(pullspec string) *MachineOSConfigBuilder {
	m.mosc.Status.CurrentImagePullspec = pullspec
	return m
}

func (m *MachineOSConfigBuilder) WithCurrentImagePullSecret(name string) *MachineOSConfigBuilder {
	m.mosc.Spec.BuildOutputs.CurrentImagePullSecret.Name = name
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

func (m *MachineOSConfigBuilder) MachineOSConfig() *mcfgv1alpha1.MachineOSConfig {
	return m.mosc.DeepCopy()
}
