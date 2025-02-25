// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigurationv1 "github.com/openshift/client-go/machineconfiguration/applyconfigurations/machineconfiguration/v1"
	typedmachineconfigurationv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	gentype "k8s.io/client-go/gentype"
)

// fakeMachineConfigPools implements MachineConfigPoolInterface
type fakeMachineConfigPools struct {
	*gentype.FakeClientWithListAndApply[*v1.MachineConfigPool, *v1.MachineConfigPoolList, *machineconfigurationv1.MachineConfigPoolApplyConfiguration]
	Fake *FakeMachineconfigurationV1
}

func newFakeMachineConfigPools(fake *FakeMachineconfigurationV1) typedmachineconfigurationv1.MachineConfigPoolInterface {
	return &fakeMachineConfigPools{
		gentype.NewFakeClientWithListAndApply[*v1.MachineConfigPool, *v1.MachineConfigPoolList, *machineconfigurationv1.MachineConfigPoolApplyConfiguration](
			fake.Fake,
			"",
			v1.SchemeGroupVersion.WithResource("machineconfigpools"),
			v1.SchemeGroupVersion.WithKind("MachineConfigPool"),
			func() *v1.MachineConfigPool { return &v1.MachineConfigPool{} },
			func() *v1.MachineConfigPoolList { return &v1.MachineConfigPoolList{} },
			func(dst, src *v1.MachineConfigPoolList) { dst.ListMeta = src.ListMeta },
			func(list *v1.MachineConfigPoolList) []*v1.MachineConfigPool {
				return gentype.ToPointerSlice(list.Items)
			},
			func(list *v1.MachineConfigPoolList, items []*v1.MachineConfigPool) {
				list.Items = gentype.FromPointerSlice(items)
			},
		),
		fake,
	}
}
