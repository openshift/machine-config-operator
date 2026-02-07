package extended

import (
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

// ClusterOperator struct is used to handle ClusterOperator resources in OCP
type ClusterOperator struct {
	template string
	Resource
}

// ClusterOperatorList struct handles list of COs
type ClusterOperatorList struct {
	ResourceList
}

// NewClusterOperator create a ClusterOperator struct
func NewClusterOperator(oc *exutil.CLI, name string) *ClusterOperator {
	return &ClusterOperator{Resource: *NewResource(oc, "co", name)}
}

// NewClusterOperatorList create a ClusterOperatorList struct
func NewClusterOperatorList(oc *exutil.CLI) *ClusterOperatorList {
	return &ClusterOperatorList{*NewResourceList(oc, "co")}
}
