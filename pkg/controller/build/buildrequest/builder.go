package buildrequest

import (
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type builder struct {
	metav1.Object
}

func newBuilder(obj metav1.Object) Builder {
	return &builder{
		Object: obj,
	}
}

func NewBuilder(obj metav1.Object) (Builder, error) {
	sel := utils.EphemeralBuildObjectSelector()

	if !sel.Matches(labels.Set(obj.GetLabels())) {
		return nil, fmt.Errorf("missing required labels: %s", sel.String())
	}

	b := newBuilder(obj)

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

func (b *builder) GetObject() metav1.Object {
	return b.Object
}

func (b *builder) MachineOSConfig() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.MachineOSConfigNameLabelKey)
}

func (b *builder) MachineOSBuild() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.MachineOSBuildNameLabelKey)
}

func (b *builder) MachineConfigPool() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.TargetMachineConfigPoolLabelKey)
}

func (b *builder) RenderedMachineConfig() (string, error) {
	return utils.GetRequiredLabelValueFromObject(b, constants.RenderedMachineConfigLabelKey)
}
