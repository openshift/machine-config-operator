package buildrequest

import (
	"context"

	corev1 "k8s.io/api/core/v1"
)

type BuildRequest interface {
	Opts() BuildRequestOpts
	BuildPod() *corev1.Pod
	Secrets() ([]*corev1.Secret, error)
	ConfigMaps() ([]*corev1.ConfigMap, error)
}

type Preparer interface {
	Prepare(context.Context) (BuildRequest, error)
	Clean(context.Context) error
}
