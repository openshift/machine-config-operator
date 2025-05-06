package imagebuilder

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
)

// A Preparer knows how to create all of the ephemeral build objects that a
// given build depends upon. These ephemeral objects include ConfigMaps,
// Secretss, etc.
type Preparer interface {
	Prepare(context.Context) (buildrequest.BuildRequest, error)
}

// An ImageBuilder knows how to Start and Stop execution of a given build as
// well as interrogate its status, and how to clean up after the build is
// terminated.
type ImageBuilder interface {
	Start(context.Context) error
	Stop(context.Context) error
	Get(context.Context) (buildrequest.Builder, error)
	ImageBuildObserver
	Cleaner
}

// A Cleaner knows how to clean up after a given running build.
type Cleaner interface {
	Clean(context.Context) error
}

// An ImageBuildObserver knows how to interrogate an executing build to
// determine what its status is as well as map that status to a given
// MachineOSBuildStatus object.
type ImageBuildObserver interface {
	Exists(context.Context) (bool, error)
	Status(context.Context) (mcfgv1.BuildProgress, error)
	MachineOSBuildStatus(context.Context) (mcfgv1.MachineOSBuildStatus, error)
}
