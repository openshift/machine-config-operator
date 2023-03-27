package framework

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// NodeLeaser is a way to ensure node exclusivity across multiple e2e test
// cases. Essentially, we want to run as many e2e test cases concurrently as
// possible. However, we don't want test cases to stomp on one another, so we
// attempt to isolate them to a single cluster node.
type NodeLeaser struct {
	// Map of nodes to their status; true means the node is available, false
	// means the node is unavailable.
	nodes map[string]bool
	// Mutex to ensure that only a single goroutine can get or release a node at
	// any given time.
	mux *sync.Mutex
	// The CoreV1Client (can be real or fake). Used for retrieving the actual
	// node object from the K8s API.
	client corev1client.CoreV1Interface
	// The default node role name (e.g., "worker")
	nodeRole string
}

const (
	nodeRoleLabelKey string = "node-role.kubernetes.io"
)

// Returns a new NodeLeaser populated with the names of nodes belonging to a
// single node role.
func NewNodeLeaser(client corev1client.CoreV1Interface, nodeRole string) (*NodeLeaser, error) {
	nodeRole = stripNodeRoleLabelKey(nodeRole)

	// We only support worker / infra node roles for now.
	if nodeRole == "master" || nodeRole == "control-plane" {
		return nil, fmt.Errorf("only roles other than \"master\" or \"control-plane\" are supported, got: %s", nodeRole)
	}

	n := &NodeLeaser{
		mux:      &sync.Mutex{},
		client:   client,
		nodeRole: nodeRole,
		nodes:    map[string]bool{},
	}

	if err := n.reconcileNodes(); err != nil {
		return nil, err
	}

	return n, nil
}

// Fetches a given node, marking it unavailable in the process. If no nodes are
// available, it will block until a node becomes available.
func (n *NodeLeaser) GetNode(t *testing.T) (*corev1.Node, error) {
	return n.getNode(t, time.Now())
}

func (n *NodeLeaser) getNode(t *testing.T, startTime time.Time) (*corev1.Node, error) {
	nodeName := n.getNodeName()
	node, err := n.client.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err == nil {
		t.Logf("Waited for node %s for %s", nodeName, time.Since(startTime))
		return node, n.reconcileNodesWithLock()
	}

	// It is possible (however unlikely) that between the time we find the next
	// available node and we query the API server for it, that it could be
	// deleted from the API server. If that's the case, we should reconcile our
	// world view and try again.
	if k8serrors.IsNotFound(err) {
		if err := n.reconcileNodesWithLock(); err != nil {
			return nil, err
		}

		return n.getNode(t, startTime)
	}

	// For any other error, we should return directly to the caller.
	return nil, err
}

// Fetches a given node, marking it unavailable in the process. This also
// returns a release func closure which can be passed to a t.Cleanup() or defer block.
func (n *NodeLeaser) GetNodeWithReleaseFunc(t *testing.T) (*corev1.Node, func(), error) {
	node, err := n.GetNode(t)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()

	releaseFunc := func() {
		n.MustReleaseNode(t, node)
		t.Logf("Node %s released; test run time: %s", node.Name, time.Since(now))
	}

	return node, releaseFunc, err
}

// Releases a given node and ensures that the node is released in a ready
// state. Will terminate the test suite if the node cannot be released.
func (n *NodeLeaser) MustReleaseNode(t *testing.T, node *corev1.Node) {
	err := n.ReleaseNode(t, node)
	if err != nil {
		t.Fatalf("unable to release node: %s", err)
	}
}

// Releases a given node and ensures that the node is released in a ready
// state, potentially reconciling our state machine at the same time.
func (n *NodeLeaser) ReleaseNode(t *testing.T, node *corev1.Node) error {
	updatedNode, err := n.client.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	var releaseErr error

	if err == nil {
		releaseErr = n.releaseKnownNode(updatedNode)
	} else {
		releaseErr = n.releaseUnknownNode(node)
	}

	if releaseErr == nil {
		t.Logf("Released node %s", node.Name)
	}

	return releaseErr
}

// Fetches all of the node names for a given MachineConfigPool and synchronizes
// our local state machine with the API servers' state. By default, all nodes
// found that are identified as being ready will be considered available.
func (n *NodeLeaser) reconcileNodes() error {
	nodeList, err := n.client.Nodes().List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			addNodeRoleLabelKey(n.nodeRole): "",
		}).String(),
	})

	if err != nil {
		return err
	}

	nextNodeMap := map[string]bool{}

	for _, node := range nodeList.Items {
		node := node

		// Build our next node map so we can figure out what nodes we need to delete.
		nextNodeMap[node.Name] = n.isNodeReady(&node)

		// If we don't know about a given node, add it and its status.
		if _, ok := n.nodes[node.Name]; !ok {
			n.nodes[node.Name] = n.isNodeReady(&node)
		}
	}

	// If we have a node that the API server doesn't, delete it.
	for nodeName := range n.nodes {
		if _, ok := nextNodeMap[nodeName]; !ok {
			delete(n.nodes, nodeName)
		}
	}

	return nil
}

// Acquires a lock before reconciling our local node state.
func (n *NodeLeaser) reconcileNodesWithLock() error {
	n.mux.Lock()
	defer n.mux.Unlock()
	return n.reconcileNodes()
}

// Releases an unknown node by forcing reconciliation.
func (n *NodeLeaser) releaseUnknownNode(node *corev1.Node) error {
	n.mux.Lock()
	defer n.mux.Unlock()

	// If we know about the node, force reconciliation which will delete the
	// unknown node.
	if _, ok := n.nodes[node.Name]; ok {
		return n.reconcileNodes()
	}

	// If we don't know about the node, return an error here.
	return fmt.Errorf("unknown node %s", node.Name)
}

// If we know about a given node, we should be able to release it.
func (n *NodeLeaser) releaseKnownNode(node *corev1.Node) error {
	if err := n.canReleaseNode(node); err != nil {
		return fmt.Errorf("unable to release node %s: %w", node.Name, err)
	}

	return n.releaseNodeName(node.Name)
}

// Determines whether a node is in a sufficient state to be released by a given test.
func (n *NodeLeaser) canReleaseNode(node *corev1.Node) error {
	keys, hasAdditionalRoles := n.nodeHasAdditionalRoles(node)
	if hasAdditionalRoles {
		return fmt.Errorf("unexpectedly has additional roles: %v", keys)
	}

	// Ensures that the node is not degraded or working.
	if !n.isNodeReady(node) {
		nodeState := node.Annotations[constants.MachineConfigDaemonStateAnnotationKey]
		return fmt.Errorf("in %s state; expected it to be %s", nodeState, constants.MachineConfigDaemonStateDone)
	}

	// Ensures that the current MachineConfig matches the desired MachineConfig
	currentMC := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
	desiredMC := node.Annotations[constants.DesiredMachineConfigAnnotationKey]
	if currentMC != desiredMC {
		return fmt.Errorf("config mismatch; current MachineConfig: %s, desired MachineConfig: %s", currentMC, desiredMC)
	}

	return nil
}

func (n *NodeLeaser) isNodeReady(node *corev1.Node) bool {
	return node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == constants.MachineConfigDaemonStateDone
}

// Acquires the first available node. If no nodes are available, this will
// block until a node becomse available.
func (n *NodeLeaser) getNodeName() string {
	for {
		node, found := n.findAndAllocateFreeNode()

		if found {
			return node
		}
		// Sleep so we don't use a ton of CPU.
		time.Sleep(time.Millisecond)
	}
}

// Releases the provided node.
func (n *NodeLeaser) releaseNodeName(node string) error {
	n.mux.Lock()
	defer n.mux.Unlock()

	if _, ok := n.nodes[node]; !ok {
		return fmt.Errorf("unknown node %s", node)
	}

	n.nodes[node] = true

	return n.reconcileNodes()
}

// Searches for the next available node.
func (n *NodeLeaser) findFreeNode() (string, bool) {
	for node, isFree := range n.nodes {
		if isFree {
			return node, true
		}
	}

	return "", false
}

// Searches for the next available node and if one is found, allocates it.
func (n *NodeLeaser) findAndAllocateFreeNode() (string, bool) {
	n.mux.Lock()
	defer n.mux.Unlock()

	node, isFree := n.findFreeNode()
	if isFree {
		n.nodes[node] = false
		return node, true
	}

	return "", false
}

// Checks that a node does not have any additional roles such as ones applied
// by one of the tests. If found, this means that the test may not have cleaned
// up after itself. This means the node might be in an inconsistent state and
// we should fail the test suite.
func (n *NodeLeaser) nodeHasAdditionalRoles(node *corev1.Node) ([]string, bool) {
	moreKeys := []string{}

	for labelKey := range node.Labels {
		if !strings.HasPrefix(labelKey, nodeRoleLabelKey) {
			continue
		}

		if n.nodeRole != stripNodeRoleLabelKey(labelKey) {
			moreKeys = append(moreKeys, labelKey)
		}
	}

	return moreKeys, len(moreKeys) != 0
}

// Idempotently concatenates the node role label (node-role.kubernetes.io/)
// with the given role. If given "worker", will return
// "node-role.kubernetes.io/worker". If given "node-role.kubernetes.io/worker",
// will return "node-role.kubernetes.io/worker".
func addNodeRoleLabelKey(in string) string {
	if !strings.HasPrefix(in, nodeRoleLabelKey) {
		return fmt.Sprintf("%s/%s", nodeRoleLabelKey, in)
	}

	return in
}

// Idempotently removes the node role label (node-role.kubernetes.io/) with the
// given role, leaving just the node role. If given
// "node-role.kubernetes.io/worker", will return "worker".
func stripNodeRoleLabelKey(in string) string {
	if !strings.HasPrefix(in, nodeRoleLabelKey) {
		return in
	}

	return strings.ReplaceAll(in, nodeRoleLabelKey+"/", "")
}
