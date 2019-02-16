package e2e_test

import (
	"testing"

	"github.com/openshift/machine-config-operator/cmd/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClusterOperatorRelatedObjects(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Errorf("%#v", err)
	}
	configClient := cb.ConfigClientOrDie("test-co-related-objects")
	co, err := configClient.ConfigV1().ClusterOperators().Get("machine-config", metav1.GetOptions{})
	if err != nil {
		t.Errorf("couldn't get clusteroperator %v", err)
	}
	if len(co.Status.RelatedObjects) == 0 {
		t.Error("expected RelatedObjects to be populated but it was not")
	}
	var foundNS bool
	for _, ro := range co.Status.RelatedObjects {
		if ro.Resource == "namespaces" && ro.Name == "openshift-machine-config-operator" {
			foundNS = true
		}
	}
	if !foundNS {
		t.Error("ClusterOperator.RelatedObjects should contain the MCO namespace")
	}
}
