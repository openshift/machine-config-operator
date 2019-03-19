package e2e_test

import (
	"testing"

	"github.com/openshift/machine-config-operator/test/e2e/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test case for https://github.com/openshift/machine-config-operator/pull/288/commits/44d5c5215b5450fca32806f796b50a3372daddc2
func TestOperatorLabel(t *testing.T) {
	cs := framework.NewClientSet("")

	d, err := cs.DaemonSets("openshift-machine-config-operator").Get("machine-config-daemon", metav1.GetOptions{})
	if err != nil {
		t.Errorf("%#v", err)
	}

	osSelector := d.Spec.Template.Spec.NodeSelector["beta.kubernetes.io/os"]
	if osSelector != "linux" {
		t.Errorf("Expected node selector 'linux', not '%s'", osSelector)
	}
}
