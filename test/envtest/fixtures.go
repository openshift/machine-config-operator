package envtest

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ControlPlaneNodeRole string = "node-role.kubernetes.io/master"
	WorkerNodeRole       string = "node-role.kubernetes.io/worker"
)

type TestFixtures struct {
	Pods           []*corev1.Pod
	Nodes          []*corev1.Node
	PodsByNodeName map[string][]*corev1.Pod
}

func newNodeWithPods(nodeName, role, podName string, numPods int) (*corev1.Node, []*corev1.Pod) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Labels: map[string]string{
				role: "",
			},
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Node",
		},
	}

	return node, newPodsForNode(node, podName, numPods)
}

func newPodsForNode(node *corev1.Node, podName string, numPods int) []*corev1.Pod {
	pods := []*corev1.Pod{}

	for i := 0; i < numPods; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", podName, i),
				Namespace: "default",
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Pod",
			},
			Spec: corev1.PodSpec{
				NodeName: node.Name,
				Containers: []corev1.Container{
					{
						Name:  podName + "-container-1",
						Image: "registry.fedora.org/fedora:latest",
					},
				},
			},
		}

		pods = append(pods, pod)
	}

	return pods
}

func NewTestFixtures() *TestFixtures {
	nodeData := map[string]map[string]string{
		ControlPlaneNodeRole: {
			"node-1": "apache",
			"node-2": "nginx",
			"node-3": "tekton",
		},
		WorkerNodeRole: {
			"node-4": "loki",
			"node-5": "prometheus",
			"node-6": "grafana",
		},
	}

	pods := []*corev1.Pod{}
	nodes := []*corev1.Node{}
	podsByNodeName := map[string][]*corev1.Pod{}

	for role, nodeDefs := range nodeData {
		for nodeName, podName := range nodeDefs {
			node, nodePods := newNodeWithPods(nodeName, role, podName, 10)
			nodes = append(nodes, node)
			pods = append(pods, nodePods...)
			podsByNodeName[node.Name] = nodePods
		}
	}

	return &TestFixtures{
		Pods:           pods,
		Nodes:          nodes,
		PodsByNodeName: podsByNodeName,
	}
}

func (tf *TestFixtures) AsRuntimeObjects() []runtime.Object {
	out := []runtime.Object{}

	for i := range tf.Nodes {
		out = append(out, tf.Nodes[i])
	}

	for i := range tf.Pods {
		out = append(out, tf.Pods[i])
	}

	return out
}
