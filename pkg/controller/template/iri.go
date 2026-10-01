package template

import (
	"fmt"
	"reflect"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// filterInternalReleaseImage re-renders when the InternalReleaseImage the merger
// looks up changes. Limits to only the singleton instance.
func (ctrl *Controller) filterInternalReleaseImage(iri *mcfgv1.InternalReleaseImage, action string) {
	if iri.Name != ctrlcommon.InternalReleaseImageInstanceName {
		return
	}
	ctrl.enqueueController()
	klog.Infof("Re-syncing ControllerConfig due to InternalReleaseImage %s %s", iri.Name, action)
}

func (ctrl *Controller) addInternalReleaseImage(obj interface{}) {
	iri := obj.(*mcfgv1.InternalReleaseImage)
	ctrl.filterInternalReleaseImage(iri, "add")
}

func (ctrl *Controller) updateInternalReleaseImage(old, cur interface{}) {
	oldIRI := old.(*mcfgv1.InternalReleaseImage)
	newIRI := cur.(*mcfgv1.InternalReleaseImage)
	if reflect.DeepEqual(oldIRI.Spec, newIRI.Spec) {
		return
	}
	ctrl.filterInternalReleaseImage(newIRI, "update")
}

func (ctrl *Controller) deleteInternalReleaseImage(obj interface{}) {
	iri, ok := obj.(*mcfgv1.InternalReleaseImage)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		iri, ok = tombstone.Obj.(*mcfgv1.InternalReleaseImage)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not an InternalReleaseImage %#v", obj))
			return
		}
	}
	// The credentials have to come back out of the rendered pull secret.
	ctrl.filterInternalReleaseImage(iri, "deletion")
}
