package internalreleaseimage

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// iriBuilder simplifies the creation of an InternalReleaseImage resource in the test.
type iriBuilder struct {
	obj *mcfgv1alpha1.InternalReleaseImage
}

func iri() *iriBuilder {
	return &iriBuilder{
		obj: &mcfgv1alpha1.InternalReleaseImage{
			ObjectMeta: v1.ObjectMeta{
				Name: common.InternalReleaseImageInstanceName,
			},
			Spec: mcfgv1alpha1.InternalReleaseImageSpec{
				Releases: []mcfgv1alpha1.InternalReleaseImageRef{
					{
						Name: "ocp-release-bundle-4.21.5-x86_64",
					},
				},
			},
		},
	}
}

func (ib *iriBuilder) build() runtime.Object {
	return ib.obj
}

// mcnBuilder simplifies the creation of a MachineConfigNode resource in the test.
type mcnBuilder struct {
	obj *mcfgv1.MachineConfigNode
}

func machineConfigNode(name string) *mcnBuilder {
	return &mcnBuilder{
		obj: &mcfgv1.MachineConfigNode{
			ObjectMeta: v1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (mb *mcnBuilder) build() runtime.Object {
	return mb.obj
}
