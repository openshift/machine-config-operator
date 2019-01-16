package e2e_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/cmd/common"
)

// Test case for https://github.com/openshift/machine-config-operator/pull/288/commits/44d5c5215b5450fca32806f796b50a3372daddc2
func TestOperatorLabel(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil{
		t.Errorf("%#v", err)
	}
	k := cb.KubeClientOrDie("sanity-test")

	d, err := k.AppsV1().DaemonSets("openshift-machine-config-operator").Get("machine-config-daemon", metav1.GetOptions{})
	if err != nil {
		t.Errorf("%#v", err)
	}

	osSelector := d.Spec.Template.Spec.NodeSelector["beta.kubernetes.io/os"]
	if osSelector != "linux" {
		t.Errorf("Expected node selector 'linux', not '%s'", osSelector)
	}
}
