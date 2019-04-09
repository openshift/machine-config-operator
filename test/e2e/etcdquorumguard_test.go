package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
)

type podstatus struct {
	node   string
	status corev1.PodPhase
}

var nodes = make(map[string]bool)
var pods = make(map[string]podstatus)

type podinfo map[string]podstatus

func TestEtcdQuorumGuard(t *testing.T) {
	cs := framework.NewClientSet("")
	if err := waitForEtcdQuorumGuardDeployment(cs); err != nil {
		fmt.Printf("No etcd-quorum-guard deployment present; assume for now it is not configured.\n")
		return
		//t.Fatal(err.Error())
	}
	fmt.Print("Make all schedulable\n")
	if err := makeAllNodesSchedulable(cs); err != nil {
		t.Errorf("Unable to make all nodes schedulable: %s", err.Error())
	}
	fmt.Print("Check for all running\n")
	if err := waitForPods(cs, 3, 3, 3); err != nil {
		t.Errorf("Unable to get all etcd-quorum-guard pods running: %s", err.Error())
	}
	fmt.Print("Make one unschedulable\n")
	if err := makeOneNodeUnschedulable(cs); err != nil {
		t.Errorf("Unable to make one node unschedulable: %s", err.Error())
	}
	fmt.Print("Wait for 2 running\n")
	if err := waitForPods(cs, 3, 2, 2); err != nil {
		t.Errorf("Unable to get one etcd-quorum-guard pod stopped: %s", err.Error())
	}
	fmt.Print("Make second unschedulable\n")
	if err := makeOneNodeUnschedulable(cs); err == nil || !strings.Contains(err.Error(), "it would violate the pod's disruption budget") {
		fmt.Print("  Pod should not have been evicted\n")
		t.Errorf("Pod should not have been evicted because it violated disruption budget: %v", err)
	} else {
		fmt.Print("  Eviction correctly failed because it would violate the pod's disruption budget.\n")
	}
	fmt.Print("Make all schedulable\n")
	if err := makeAllNodesSchedulable(cs); err != nil {
		t.Errorf("Unable to make all nodes schedulable: %s", err.Error())
	}
	fmt.Print("Wait for all running\n")
	if err := waitForPods(cs, 3, 3, 3); err != nil {
		t.Errorf("Unable to get all etcd-quorum-guard pods running: %s", err.Error())
	}
	fmt.Print("Make one unschedulable\n")
	if err := makeOneNodeUnschedulable(cs); err != nil {
		t.Errorf("Unable to make one node unschedulable: %s", err.Error())
	}
	fmt.Print("Wait for one not running\n")
	if err := waitForPods(cs, 3, 2, 2); err != nil {
		t.Errorf("Unable to get one etcd-quorum-guard pod stopped: %s", err.Error())
	}
	fmt.Print("Make all schedulable\n")
	if err := makeAllNodesSchedulable(cs); err != nil {
		t.Errorf("Unable to make all nodes schedulable: %s", err.Error())
	}
	fmt.Print("Wait for all\n")
	if err := waitForPods(cs, 3, 3, 3); err != nil {
		t.Errorf("Unable to get all etcd-quorum-guard pods running: %s", err.Error())
	}
}

func makeNodeUnSchedulableOrSchedulable(cs *framework.ClientSet, node string, unschedulable bool) error {
	prefix := ""
	if unschedulable {
		prefix = "un"
	}
	for {
		n, err := getNode(cs, node)
		if err != nil {
			return err
		}
		if n.Spec.Unschedulable != unschedulable {
			n.Spec.Unschedulable = !unschedulable
			if _, err := cs.CoreV1Interface.Nodes().Update(n); err != nil {
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

func makeAllNodesSchedulable(cs *framework.ClientSet) error {
	for node, unschedulable := range nodes {
		if unschedulable {
			err := makeNodeUnSchedulableOrSchedulable(cs, node, false)
			if err != nil {
				return err
			}
			nodes[node] = false
		}
	}
	return getMasterNodes(cs)
}

func evictEtcdQuotaGuardPodsFromNode(cs *framework.ClientSet, node string) error {
	pods, err := getEtcdQuotaGuardPodsOnNode(cs, node)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		fmt.Printf("  Evicting pod %s/%s...\n", pod.ObjectMeta.Namespace, pod.ObjectMeta.Name)
		err = cs.CoreV1Interface.Pods(pod.ObjectMeta.Namespace).Evict(&policyv1beta1.Eviction{metav1.TypeMeta{}, pod.ObjectMeta, &metav1.DeleteOptions{}})
		if err != nil {
			err = fmt.Errorf("     Unable to evict pod %s/%s: %s\n", pod.ObjectMeta.Namespace, pod.ObjectMeta.Name, err.Error())
		}
	}
	return err
}

func makeOneNodeUnschedulable(cs *framework.ClientSet) error {
	var err error
	for node, unschedulable := range nodes {
		if !unschedulable {
			err = makeNodeUnSchedulableOrSchedulable(cs, node, true)
			if err != nil {
				break
			}
			nodes[node] = true
			err = evictEtcdQuotaGuardPodsFromNode(cs, node)
			break
		}
	}
	err1 := getMasterNodes(cs)
	if err != nil {
		return err
	}
	return err1

}

func getNode(cs *framework.ClientSet, node string) (*corev1.Node, error) {
	return cs.CoreV1Interface.Nodes().Get(node, metav1.GetOptions{})
}

func waitForEtcdQuorumGuardDeployment(cs *framework.ClientSet) error {
	err := wait.PollImmediate(5*time.Second, 30*time.Second, func() (bool, error) {
		_, err := cs.AppsV1Interface.Deployments("kube-system").Get("etcd-quorum-guard", metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		fmt.Printf("  error waiting for etcd-quorum-guard deployment to exist: %v\n", err)
		return false, nil
	})
	return err
}

func waitForPods(cs *framework.ClientSet, expectedTotal, min, max int32) error {
	err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		d, err := cs.AppsV1Interface.Deployments("kube-system").Get("etcd-quorum-guard", metav1.GetOptions{})
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

func getMasterNodes(cs *framework.ClientSet) error {
	n, err := cs.CoreV1Interface.Nodes().List(metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
	if err != nil {
		return err
	}
	for _, no := range n.Items {
		nodes[no.ObjectMeta.Name] = no.Spec.Unschedulable
	}
	return nil
}

func getEtcdQuotaGuardPodsOnNode(cs *framework.ClientSet, node string) ([]corev1.Pod, error) {
	_, err := getNode(cs, node)
	var answer []corev1.Pod
	if err != nil {
		return answer, fmt.Errorf("No such node %s", node)
	}
	p, err := cs.CoreV1Interface.Pods("kube-system").List(metav1.ListOptions{LabelSelector: "name=etcd-quorum-guard"})
	for _, pod := range p.Items {
		if pod.Spec.NodeName == node {
			answer = append(answer, pod)
		}
	}
	return answer, nil
}

func getEtcdQuotaGuardPods(cs *framework.ClientSet) error {
	p, err := cs.CoreV1Interface.Pods("kube-system").List(metav1.ListOptions{LabelSelector: "name=etcd-quorum-guard"})
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
