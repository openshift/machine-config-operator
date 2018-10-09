package resourcemerge

import (
	osv1 "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

func EnsureOperatorStatus(modified *bool, existing *osv1.OperatorStatus, required osv1.OperatorStatus) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	if !equality.Semantic.DeepEqual(existing.Condition, required.Condition) {
		*modified = true
		existing.Condition = required.Condition
	}
	if existing.Version != required.Version {
		*modified = true
		existing.Version = required.Version
	}
	if !existing.LastUpdate.Equal(&required.LastUpdate) {
		*modified = true
		existing.LastUpdate = required.LastUpdate
	}
	if !equality.Semantic.DeepEqual(existing.Extension.Raw, required.Extension.Raw) {
		*modified = true
		existing.Extension.Raw = required.Extension.Raw
	}
	if !equality.Semantic.DeepEqual(existing.Extension.Object, required.Extension.Object) {
		*modified = true
		existing.Extension.Object = required.Extension.Object
	}
}
