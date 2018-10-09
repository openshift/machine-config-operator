package resourcemerge

import (
	cvv1 "github.com/openshift/cluster-version-operator/pkg/apis/clusterversion.openshift.io/v1"
)

func EnsureCVOConfig(modified *bool, existing *cvv1.CVOConfig, required cvv1.CVOConfig) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	if existing.Upstream != required.Upstream {
		*modified = true
		existing.Upstream = required.Upstream
	}
	if existing.Channel != required.Channel {
		*modified = true
		existing.Channel = required.Channel
	}
	if existing.ClusterID != required.ClusterID {
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
