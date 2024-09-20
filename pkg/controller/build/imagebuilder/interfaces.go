package imagebuilder

import (
	"context"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
)

type Preparer interface {
	Prepare(context.Context) (buildrequest.BuildRequest, error)
}

type ImageBuilder interface {
	Start(context.Context) error
	Stop(context.Context) error
	ImageBuildObserver
	Cleaner
}

type Cleaner interface {
	Clean(context.Context) error
}

type ImageBuildObserver interface {
	Status(context.Context) (mcfgv1alpha1.BuildProgress, error)
	MachineOSBuildStatus(context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error)
}
