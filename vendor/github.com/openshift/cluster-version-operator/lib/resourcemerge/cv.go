package resourcemerge

import (
	"github.com/openshift/cluster-version-operator/pkg/apis/clusterversion.openshift.io/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

func EnsureOperatorStatus(modified *bool, existing *v1.OperatorStatus, required v1.OperatorStatus) {
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

func EnsureCVOConfig(modified *bool, existing *v1.CVOConfig, required v1.CVOConfig) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	if existing.Upstream != required.Upstream {
		*modified = true
		existing.Upstream = required.Upstream
	}
	if existing.Channel != required.Channel {
		*modified = true
		existing.Channel = required.Channel
	}
	if existing.ClusterID.String() != required.ClusterID.String() {
		*modified = true
		existing.ClusterID = required.ClusterID
	}

	if required.DesiredUpdate.Payload != "" &&
		existing.DesiredUpdate.Payload != required.DesiredUpdate.Payload {
		*modified = true
		existing.DesiredUpdate.Payload = required.DesiredUpdate.Payload
	}
	if required.DesiredUpdate.Version != "" &&
		existing.DesiredUpdate.Version != required.DesiredUpdate.Version {
		*modified = true
		existing.DesiredUpdate.Version = required.DesiredUpdate.Version
	}
}
