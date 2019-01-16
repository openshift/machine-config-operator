package e2e_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/informers"
	"k8s.io/apimachinery/pkg/labels"

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
	k := cb.KubeClientOrDie("sanity-test")

	kubeSharedInformer := informers.NewSharedInformerFactory(k, 20 * time.Minute)
	nodeInformer := kubeSharedInformer.Core().V1().Nodes()
	nodeLister := nodeInformer.Lister()
	nodeListerSynced := nodeInformer.Informer().HasSynced

	stopCh := make(chan struct{})
	kubeSharedInformer.Start(stopCh)
	cache.WaitForCacheSync(stopCh, nodeListerSynced)

	nodes, err := nodeLister.List(labels.Everything())
	if err != nil {
		t.Errorf("listing nodes: %v", err)
	}

	var degraded []*corev1.Node
	for _, node := range nodes {
		if node.Annotations == nil {
			continue
		}
		dstate, ok := node.Annotations[daemon.MachineConfigDaemonStateAnnotationKey]
		if !ok || dstate == "" {
			continue
		}

		if dstate == daemon.MachineConfigDaemonStateDegraded {
			degraded = append(degraded, node)
		}
	}

	if len(degraded) > 0 {
		t.Errorf("%d degraded nodes found", len(degraded))
	}
}
