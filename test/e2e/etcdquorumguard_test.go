package e2e

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"strings"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/test/e2e/framework"
	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
)

type podstatus struct {
	node   string
	status corev1.PodPhase
}

var nodes = make(map[string]bool)
var pods = make(map[string]podstatus)

type podinfo map[string]podstatus

// TestEtcdQuorumGuard tests the etcd Quorum Guard.  It assumes there
// are exactly three master pods (as does the etcd Quorum Guard at
// present).  The test first makes one node unschedulable and evicts
// the EQG pod from it, ensuring that eviction succeeds.  The test
// next makes a second node unschedulable and then attempts to evict
// the EQG pod from it.  It checks that the pod is *not* evicted.  It
// then makes all nodes schedulable and checks that the EQG pod is
// present/restarted on all masters.  It then makes one node
// unschedulable again and checks that the EQG pod is evicted.
func TestEtcdQuorumGuard(t *testing.T) {
	cs := framework.NewClientSet("")
	if err := waitForEtcdQuorumGuardDeployment(cs); err != nil {
		t.Fatalf("etcdQuorumGuard deployment not present: %s", err.Error())
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
	if err := makeOneNodeUnschedulableAndEvict(cs); err != nil {
		t.Errorf("Unable to make one node unschedulable: %s", err.Error())
	}
	fmt.Print("Wait for 2 running\n")
	if err := waitForPods(cs, 3, 2, 2); err != nil {
		t.Errorf("Unable to get one etcd-quorum-guard pod stopped: %s", err.Error())
	}
	fmt.Print("Make second unschedulable\n")
	if err := makeOneNodeUnschedulableAndEvict(cs); err == nil || !strings.Contains(err.Error(), "it would violate the pod's disruption budget") {
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
	if err := makeOneNodeUnschedulableAndEvict(cs); err != nil {
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
		if n.Spec.Unschedulable == unschedulable {
			fmt.Printf("  Node %s is already %sschedulable\n", node, prefix)
			return nil
		}
		n.Spec.Unschedulable = unschedulable
		if _, err := cs.CoreV1Interface.Nodes().Update(context.TODO(), n, metav1.UpdateOptions{}); err != nil {
			if strings.Contains(err.Error(), "the object has been modified") {
				fmt.Print("    Node object was modified and not up to date; retrying\n")
				continue
			}
			return errors.Wrapf(err, "failed to make node %s %sschedulable", node, prefix)
		}
		break
	}
	return wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		if err := getMasterNodes(cs); err != nil {
			fmt.Printf("Error getting master nodes: %s\n", err.Error())
			return true, err
		}
		n, err := getNode(cs, node)
		if err != nil {
			fmt.Printf("Error getting node status for %s: %s\n", node, err.Error())
			return true, err
		}
		if n.Spec.Unschedulable == unschedulable {
			return true, nil
		}
		fmt.Printf("Node %s not yet %sschedulable\n", node, prefix)
		return false, nil
	})
}

func makeAllNodesSchedulable(cs *framework.ClientSet) error {
	if err := getMasterNodes(cs); err != nil {
		fmt.Printf("Error getting master nodes %s\n", err.Error())
		return err
	}
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
	var podErrs []error
	for _, pod := range pods {
		fmt.Printf("  Evicting pod %s/%s/%s...\n", node, pod.ObjectMeta.Namespace, pod.ObjectMeta.Name)
		err = cs.CoreV1Interface.Pods(pod.ObjectMeta.Namespace).Evict(context.TODO(), &policyv1beta1.Eviction{metav1.TypeMeta{}, pod.ObjectMeta, &metav1.DeleteOptions{}})
		if err != nil {
			podErrs = append(podErrs, errors.Wrapf(err, "Unable to evict pod %s/%s", pod.ObjectMeta.Namespace, pod.ObjectMeta.Name))
		}
	}
	return utilerrors.NewAggregate(podErrs)
}

// makeOneNodeUnschedulableAndEvict attempts to evict the etcd Quorum
// Guard pod from a node after making it unschedulable.
func makeOneNodeUnschedulableAndEvict(cs *framework.ClientSet) error {
	var err error
	for node, unschedulable := range nodes {
		if !unschedulable {
			err = makeNodeUnSchedulableOrSchedulable(cs, node, true)
			if err != nil {
				fmt.Printf("    Make %s unschedulable failed: %s\n", node, err.Error())
				break
			}
			nodes[node] = true
			err = evictEtcdQuotaGuardPodsFromNode(cs, node)
			break
		}
	}
	// Always update the list of master nodes regardless of whether there
	// was an earlier error.  If there was an earlier error, return that;
	// otherwise return any error that getMasterNodes() produced.
	err1 := getMasterNodes(cs)
	if err != nil {
		return err
	}
	return err1

}

func getNode(cs *framework.ClientSet, node string) (*corev1.Node, error) {
	return cs.CoreV1Interface.Nodes().Get(context.TODO(), node, metav1.GetOptions{})
}

func waitForEtcdQuorumGuardDeployment(cs *framework.ClientSet) error {
	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		_, err := cs.AppsV1Interface.Deployments("openshift-machine-config-operator").Get(context.TODO(), "etcd-quorum-guard", metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		fmt.Printf("  error waiting for etcd-quorum-guard deployment to exist: %v\n", err)
		return false, nil
	})
	return err
}

// waitForPods waits for the expected number of etcd Quorum Guard pods
// to be present and for the number of available pods to be within the
// specified bounds.
func waitForPods(cs *framework.ClientSet, expectedTotal, min, max int32) error {
	err := wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		d, err := cs.AppsV1Interface.Deployments("openshift-machine-config-operator").Get(context.TODO(), "etcd-quorum-guard", metav1.GetOptions{})
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
				return fmt.Errorf("pod %s running on %s, not a master", pod, node)
			}
		}
	}
	return nil
}

func getMasterNodes(cs *framework.ClientSet) error {
	n, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
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
	p, err := cs.CoreV1Interface.Pods("openshift-machine-config-operator").List(context.TODO(), metav1.ListOptions{LabelSelector: "name=etcd-quorum-guard"})
	for _, pod := range p.Items {
		if pod.Spec.NodeName == node {
			answer = append(answer, pod)
		}
	}
	return answer, nil
}

func getEtcdQuotaGuardPods(cs *framework.ClientSet) error {
	p, err := cs.CoreV1Interface.Pods("openshift-machine-config-operator").List(context.TODO(), metav1.ListOptions{LabelSelector: "name=etcd-quorum-guard"})
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
