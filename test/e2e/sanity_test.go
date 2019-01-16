package e2e_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"
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

func TestNoDegraded(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil{
		t.Errorf("%#v", err)
	}
	client := cb.KubeClientOrDie("sanity-test")

	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		t.Errorf("listing nodes: %v", err)
	}

	var degraded []*corev1.Node
	for _, node := range nodes.Items {
		err := wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
			if node.Annotations == nil {
				return false, nil
			}
			dstate, ok := node.Annotations[daemon.MachineConfigDaemonStateAnnotationKey]
			if !ok || dstate == "" {
				return false, nil
			}
			if dstate == daemon.MachineConfigDaemonStateDegraded {
				degraded = append(degraded, &node)
			}
			return true, nil
		})
		if err != nil {
			t.Errorf("node annotation: %v", err)
		}
	}

	if len(degraded) > 0 {
		t.Errorf("%d degraded nodes found", len(degraded))
	}
}
