package buildrequest

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type BuildRequest interface {
	Opts() BuildRequestOpts
	Builder() Builder
	Secrets() ([]*corev1.Secret, error)
	ConfigMaps() ([]*corev1.ConfigMap, error)
}

// Thin wrapper on top of Pods and other executable Kube
// objects.
//
// TODO: Figure out how to distinguish between this and ephemeral build objects
// such as ConfigMaps and Secrets.
type Builder interface {
	MachineOSConfig() (string, error)
	MachineOSBuild() (string, error)
	MachineConfigPool() (string, error)
	RenderedMachineConfig() (string, error)
	GetObject() metav1.Object
	metav1.Object
}
