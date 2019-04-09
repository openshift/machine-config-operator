package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type podstatus struct {
	node   string
	status corev1.PodPhase
}

var nodes = make(map[string]bool)
var pods = make(map[string]podstatus)

type podinfo map[string]podstatus

func TestEtcdQuorumGuard(t *testing.T) {
	kclient, err := initialize()
	if err != nil {
		fmt.Printf("No etcd-quorum-guard deployment present; assume for now it is not configured.\n")
		return
		//t.Fatal(err.Error())
	}
	fmt.Print("Make all schedulable\n")
	if err = makeAllNodesSchedulable(kclient); err != nil {
		t.Errorf("Unable to make all nodes schedulable: %s", err.Error())
	}
	fmt.Print("Check for all running\n")
	if err = waitForPods(kclient, 3, 3, 3); err != nil {
		t.Errorf("Unable to get all etcd-quorum-guard pods running: %s", err.Error())
	}
	fmt.Print("Make one unschedulable\n")
	if err = makeOneNodeUnschedulable(kclient); err != nil {
		t.Errorf("Unable to make one node unschedulable: %s", err.Error())
	}
	fmt.Print("Wait for 2 running\n")
	if err = waitForPods(kclient, 3, 2, 2); err != nil {
		t.Errorf("Unable to get one etcd-quorum-guard pod stopped: %s", err.Error())
	}
	fmt.Print("Make second unschedulable\n")
	if err = makeOneNodeUnschedulable(kclient); err == nil || !strings.Contains(err.Error(), "it would violate the pod's disruption budget") {
		fmt.Print("  Pod should not have been evicted\n")
		t.Errorf("Pod should not have been evicted because it violated disruption budget: %v", err)
	} else {
		fmt.Print("  Eviction correctly failed because it would violate the pod's disruption budget.\n")
	}
	fmt.Print("Make all schedulable\n")
	if err = makeAllNodesSchedulable(kclient); err != nil {
		t.Errorf("Unable to make all nodes schedulable: %s", err.Error())
	}
	fmt.Print("Wait for all running\n")
	if err = waitForPods(kclient, 3, 3, 3); err != nil {
		t.Errorf("Unable to get all etcd-quorum-guard pods running: %s", err.Error())
	}
	fmt.Print("Make one unschedulable\n")
	if err = makeOneNodeUnschedulable(kclient); err != nil {
		t.Errorf("Unable to make one node unschedulable: %s", err.Error())
	}
	fmt.Print("Wait for one not running\n")
	if err = waitForPods(kclient, 3, 2, 2); err != nil {
		t.Errorf("Unable to get one etcd-quorum-guard pod stopped: %s", err.Error())
	}
	fmt.Print("Make all schedulable\n")
	if err = makeAllNodesSchedulable(kclient); err != nil {
		t.Errorf("Unable to make all nodes schedulable: %s", err.Error())
	}
	fmt.Print("Wait for all\n")
	if err = waitForPods(kclient, 3, 3, 3); err != nil {
		t.Errorf("Unable to get all etcd-quorum-guard pods running: %s", err.Error())
	}
}

func initialize() (*k8sclient.Clientset, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("Error initializing kubeconfig: %s\n", err.Error())
	}
	kclient, err := k8sclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("Error creating client: %s\n", err.Error())
	}
	err = getMasterNodes(kclient)
	if err != nil {
		return nil, fmt.Errorf("Error getting master nodes: %s", err.Error())
	}

	err = waitForEtcdQuorumGuardDeployment(kclient)
	if err != nil {
		return nil, fmt.Errorf("Error getting etcd quota guard deployment: %s", err.Error())
	}
	// e2e test job does not guarantee our operator is up before
	// launching the test, so we need to do so.
	err = getEtcdQuotaGuardPods(kclient)
	if err != nil {
		return nil, fmt.Errorf("Error getting pods: %s", err.Error())
	}
	return kclient, nil
}

func makeNodeUnSchedulableOrSchedulable(kclient *k8sclient.Clientset, node string, unschedulable bool) error {
	prefix := ""
	if unschedulable {
		prefix = "un"
	}
	for {
		n, err := getNode(kclient, node)
		if err != nil {
			return err
		}
		if n.Spec.Unschedulable != unschedulable {
			n.Spec.Unschedulable = !unschedulable
			if _, err := kclient.CoreV1().Nodes().Update(n); err != nil {
				if strings.Contains(err.Error(), "the object has been modified") {
					fmt.Print("    Node object was modified and not up to date; retrying\n")
					continue;
				}
				return fmt.Errorf("Failed to make node %s %sschedulable: %s\n", node, prefix, err.Error())
			}
		} else {
			fmt.Printf("  Node %s is already %sschedulable", node, prefix)
		}
		return nil
	}
}

func makeAllNodesSchedulable(kclient *k8sclient.Clientset) error {
	for node, unschedulable := range nodes {
		if unschedulable {
			err := makeNodeUnSchedulableOrSchedulable(kclient, node, false)
			if err != nil {
				return err
			}
			nodes[node] = false
		}
	}
	return getMasterNodes(kclient)
}

func evictEtcdQuotaGuardPodsFromNode(kclient *k8sclient.Clientset, node string) error {
	pods, err := getEtcdQuotaGuardPodsOnNode(kclient, node)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		fmt.Printf("  Evicting pod %s/%s...\n", pod.ObjectMeta.Namespace, pod.ObjectMeta.Name)
		err = kclient.CoreV1().Pods(pod.ObjectMeta.Namespace).Evict(&policyv1beta1.Eviction{metav1.TypeMeta{}, pod.ObjectMeta, &metav1.DeleteOptions{}})
		if err != nil {
			err = fmt.Errorf("     Unable to evict pod %s/%s: %s\n", pod.ObjectMeta.Namespace, pod.ObjectMeta.Name, err.Error())
		}
	}
	return err
}

func makeOneNodeUnschedulable(kclient *k8sclient.Clientset) error {
	var err error
	for node, unschedulable := range nodes {
		if !unschedulable {
			err = makeNodeUnSchedulableOrSchedulable(kclient, node, true)
			if err != nil {
				break
			}
			nodes[node] = true
			err = evictEtcdQuotaGuardPodsFromNode(kclient, node)
			break
		}
	}
	err1 := getMasterNodes(kclient)
	if err != nil {
		return err
	}
	return err1

}

func getNode(kclient *k8sclient.Clientset, node string) (*corev1.Node, error) {
	return kclient.CoreV1().Nodes().Get(node, metav1.GetOptions{})
}

func waitForEtcdQuorumGuardDeployment(kclient *k8sclient.Clientset) error {
	err := wait.PollImmediate(5*time.Second, 30*time.Second, func() (bool, error) {
		_, err := kclient.AppsV1().Deployments("kube-system").Get("etcd-quorum-guard", metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		fmt.Printf("  error waiting for etcd-quorum-guard deployment to exist: %v\n", err)
		return false, nil
	})
	return err
}

func waitForPods(kclient *k8sclient.Clientset, expectedTotal, min, max int32) error {
	err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		d, err := kclient.AppsV1().Deployments("kube-system").Get("etcd-quorum-guard", metav1.GetOptions{})
		if err != nil {
			// By this point the deployment should exist.
			fmt.Printf("  error waiting for etcd-quorum-guard deployment to exist: %v\n", err)
			return true, err
		}
		if d.Status.Replicas < 1 {
			fmt.Println("operator deployment has no replicas")
			return false, nil
		}
		if d.Status.Replicas == expectedTotal &&
			d.Status.AvailableReplicas >= min &&
			d.Status.AvailableReplicas <= max {
			fmt.Printf("  Deployment is ready! %d %d\n", d.Status.Replicas, d.Status.AvailableReplicas)
			return true, nil
		}
		//fmt.Printf("  Deployment is not ready! %d %d\n", d.Status.Replicas, d.Status.AvailableReplicas)
		return false, nil
	})
	if err != nil {
		return err
	}
	for pod, info := range pods {
		if info.status == "Running" {
			node := info.node
			if node == "" {
				return fmt.Errorf("Pod %s not associated with a node", pod)
			}
			if _, ok := nodes[node]; !ok {
				return fmt.Errorf("Pod %s running on %s, not a master!", pod, node)
			}
		}
	}
	return nil
}

func getMasterNodes(kclient *k8sclient.Clientset) error {
	n, err := kclient.CoreV1().Nodes().List(metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
	if err != nil {
		return err
	}
	for _, no := range n.Items {
		nodes[no.ObjectMeta.Name] = no.Spec.Unschedulable
	}
	return nil
}

func getEtcdQuotaGuardPodsOnNode(kclient *k8sclient.Clientset, node string) ([]corev1.Pod, error) {
	_, err := getNode(kclient, node)
	var answer []corev1.Pod
	if err != nil {
		return answer, fmt.Errorf("No such node %s", node)
	}
	p, err := kclient.CoreV1().Pods("kube-system").List(metav1.ListOptions{LabelSelector: "name=etcd-quorum-guard"})
	for _, pod := range p.Items {
		if pod.Spec.NodeName == node {
			answer = append(answer, pod)
		}
	}
	return answer, nil
}

func getEtcdQuotaGuardPods(kclient *k8sclient.Clientset) error {
	p, err := kclient.CoreV1().Pods("kube-system").List(metav1.ListOptions{LabelSelector: "name=etcd-quorum-guard"})
	if err != nil {
		return err
	}
	for _, po := range p.Items {
		pods[po.ObjectMeta.Name] = podstatus{
			node:   po.Spec.NodeName,
			status: po.Status.Phase,
		}
	}
	return nil
}
