// Code generated by client-gen. DO NOT EDIT.

package v1

import (
	context "context"

	machinev1 "github.com/openshift/api/machine/v1"
	applyconfigurationsmachinev1 "github.com/openshift/client-go/machine/applyconfigurations/machine/v1"
	scheme "github.com/openshift/client-go/machine/clientset/versioned/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"
)

// ControlPlaneMachineSetsGetter has a method to return a ControlPlaneMachineSetInterface.
// A group's client should implement this interface.
type ControlPlaneMachineSetsGetter interface {
	ControlPlaneMachineSets(namespace string) ControlPlaneMachineSetInterface
}

// ControlPlaneMachineSetInterface has methods to work with ControlPlaneMachineSet resources.
type ControlPlaneMachineSetInterface interface {
	Create(ctx context.Context, controlPlaneMachineSet *machinev1.ControlPlaneMachineSet, opts metav1.CreateOptions) (*machinev1.ControlPlaneMachineSet, error)
	Update(ctx context.Context, controlPlaneMachineSet *machinev1.ControlPlaneMachineSet, opts metav1.UpdateOptions) (*machinev1.ControlPlaneMachineSet, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, controlPlaneMachineSet *machinev1.ControlPlaneMachineSet, opts metav1.UpdateOptions) (*machinev1.ControlPlaneMachineSet, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*machinev1.ControlPlaneMachineSet, error)
	List(ctx context.Context, opts metav1.ListOptions) (*machinev1.ControlPlaneMachineSetList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *machinev1.ControlPlaneMachineSet, err error)
	Apply(ctx context.Context, controlPlaneMachineSet *applyconfigurationsmachinev1.ControlPlaneMachineSetApplyConfiguration, opts metav1.ApplyOptions) (result *machinev1.ControlPlaneMachineSet, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, controlPlaneMachineSet *applyconfigurationsmachinev1.ControlPlaneMachineSetApplyConfiguration, opts metav1.ApplyOptions) (result *machinev1.ControlPlaneMachineSet, err error)
	ControlPlaneMachineSetExpansion
}

// controlPlaneMachineSets implements ControlPlaneMachineSetInterface
type controlPlaneMachineSets struct {
	*gentype.ClientWithListAndApply[*machinev1.ControlPlaneMachineSet, *machinev1.ControlPlaneMachineSetList, *applyconfigurationsmachinev1.ControlPlaneMachineSetApplyConfiguration]
}

// newControlPlaneMachineSets returns a ControlPlaneMachineSets
func newControlPlaneMachineSets(c *MachineV1Client, namespace string) *controlPlaneMachineSets {
	return &controlPlaneMachineSets{
		gentype.NewClientWithListAndApply[*machinev1.ControlPlaneMachineSet, *machinev1.ControlPlaneMachineSetList, *applyconfigurationsmachinev1.ControlPlaneMachineSetApplyConfiguration](
			"controlplanemachinesets",
			c.RESTClient(),
			scheme.ParameterCodec,
			namespace,
			func() *machinev1.ControlPlaneMachineSet { return &machinev1.ControlPlaneMachineSet{} },
			func() *machinev1.ControlPlaneMachineSetList { return &machinev1.ControlPlaneMachineSetList{} },
		),
	}
}
