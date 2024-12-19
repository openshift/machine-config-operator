package buildrequest

import (
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// The Builder interface is meant to be a light wrapper around Pods, Jobs, or
// any other Kube object which represents a running build. The idea behind is
// to allow consistent retrieval of any metadata attached to it, such as the
// MachineOSBuild name, MachineOSConfig name, rendered MachineConfig name, or
// MachineConfigPool name.
type builder struct {
	metav1.Object
}

// Constructs a new Builder object but does not perform validation.
func newBuilder(obj metav1.Object, err error) (Builder, error) {
	return &builder{
		Object: obj,
	}, err
}

// Constructs a new Builder object and ensures that it has all of the needed
// metadata.
func NewBuilder(obj metav1.Object) (Builder, error) {
	sel := utils.EphemeralBuildObjectSelector()

	if !sel.Matches(labels.Set(obj.GetLabels())) {
		return nil, fmt.Errorf("missing required labels: %s", sel.String())
	}

	b, err := newBuilder(obj, nil)
	if err != nil {
		return nil, err
	}

	if _, err := b.MachineOSConfig(); err != nil {
		return nil, err
	}

	if _, err := b.MachineOSBuild(); err != nil {
		return nil, err
	}

	if _, err := b.MachineConfigPool(); err != nil {
		return nil, err
	}

	if _, err := b.RenderedMachineConfig(); err != nil {
		return nil, err
	}

	if _, err := b.MachineOSBuild(); err != nil {
		return nil, err
	}

	return b, nil
}

// Returns the underlying object as a metav1.Object.
func (b *builder) GetObject() metav1.Object {
	return b.Object
}

// Gets the name of the MachineOSConfig this Builder object belongs to.
func (b *builder) MachineOSConfig() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.MachineOSConfigNameLabelKey)
}

// Gets the name of the MachineOSBuild this Builder object belongs to.
func (b *builder) MachineOSBuild() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.MachineOSBuildNameLabelKey)
}

// Gets the name of the MachineConfigPool this Builder object belongs to.
func (b *builder) MachineConfigPool() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.TargetMachineConfigPoolLabelKey)
}

// Gets the name of the rendered MachineConfig this Builder is using.
func (b *builder) RenderedMachineConfig() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.RenderedMachineConfigLabelKey)
}
