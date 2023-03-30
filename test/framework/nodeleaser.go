package framework

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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

// Returns a new NodeLeaser populated with the names of nodes belonging to a
// single node role.
func NewNodeLeaser(client corev1client.CoreV1Interface, nodeRole string) (*NodeLeaser, error) {
	n := &NodeLeaser{
		mux:      &sync.Mutex{},
		client:   client,
		nodeRole: nodeRole,
	}

	nodeMap, err := n.getNodeNamesForPool()
	if err != nil {
		return nil, err
	}

	n.nodes = nodeMap
	return n, nil
}

// Acquires the first available node. If no nodes are available, this will
// block until a node becomes available. Also returns a release closure which
// has all the necessary context to release the node.
func (n *NodeLeaser) GetNodeWithReleaseFunc(t *testing.T) (*corev1.Node, func(), error) {
	node, err := n.GetNode(t)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()

	releaseFunc := func() {
		// Since we already know the node name (and we have a *testing.T
		// available), we can require that there be no error here.
		require.NoError(t, n.releaseNodeName(node.Name))
		t.Logf("Node %s released; test run time: %s", node.Name, time.Since(now))
	}

	return node, releaseFunc, err
}

// Acquires the first available node. If no nodes are available, this will
// block until a node becomes available.
func (n *NodeLeaser) GetNode(t *testing.T) (*corev1.Node, error) {
	now := time.Now()
	nodeName, err := n.getNodeName()
	if err != nil {
		return nil, err
	}

	node, err := n.client.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})

	keys, hasAdditionalRoles := n.nodeHasAdditionalRoles(node)
	if hasAdditionalRoles {
		return nil, fmt.Errorf("node %s has additional roles: %v", node.Name, keys)
	}

	if err == nil {
		t.Logf("Acquired node %s after %s", nodeName, time.Since(now))
	}

	return node, err
}

func (n *NodeLeaser) ReleaseNode(t *testing.T, node *corev1.Node) error {
	updatedNode, err := n.client.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	keys, hasAdditionalRoles := n.nodeHasAdditionalRoles(updatedNode)
	if hasAdditionalRoles {
		return fmt.Errorf("test %s failed to remove additional role(s) (%v) from node %s", t.Name(), keys, updatedNode.Name)
	}

	return n.releaseNodeName(updatedNode.Name)
}

// Acquires the first available node. If no nodes are available, this will
// block until a node becomse available.
func (n *NodeLeaser) getNodeName() (string, error) {
	for {
		nodeName, found := n.findFreeNode()
		if found {
			return nodeName, nil
		}

		// Sleep so we don't use a ton of CPU.
		time.Sleep(time.Millisecond)
	}
}

func (n *NodeLeaser) removeAdditionalRolesFromNode(node *corev1.Node) error {
	keys, hasAdditionalRoles := n.nodeHasAdditionalRoles(node)
	if !hasAdditionalRoles {
		return nil
	}

	for _, key := range keys {
		delete(node.Labels, key)
	}

	_, err := n.client.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
	return err
}

func (n *NodeLeaser) nodeHasAdditionalRoles(node *corev1.Node) ([]string, bool) {
	keysToDelete := []string{}

	for labelKey := range node.Labels {
		if strings.Contains(labelKey, "node-role.kubernetes.io") && !strings.Contains(labelKey, n.nodeRole) {
			keysToDelete = append(keysToDelete, labelKey)
		}
	}

	return keysToDelete, len(keysToDelete) != 0
}

// Releases the provided node.
func (n *NodeLeaser) releaseNodeName(node string) error {
	n.mux.Lock()
	defer n.mux.Unlock()
	if _, ok := n.nodes[node]; !ok {
		return fmt.Errorf("unknown node %s", node)
	}

	n.nodes[node] = true
	return nil
}

// Finds the next available node and if one is found, it gets marked as
// unavailable.
func (n *NodeLeaser) findFreeNode() (string, bool) {
	n.mux.Lock()
	defer n.mux.Unlock()

	for node, isFree := range n.nodes {
		if isFree {
			n.nodes[node] = false
			return node, true
		}
	}

	return "", false
}

func (n *NodeLeaser) getNodeNamesForPool() (map[string]bool, error) {
	nodeList, err := n.client.Nodes().List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			fmt.Sprintf("node-role.kubernetes.io/%s", n.nodeRole): "",
		}).String(),
	})

	if err != nil {
		return nil, err
	}

	nodeMap := map[string]bool{}
	for _, node := range nodeList.Items {
		nodeMap[node.Name] = true
	}

	return nodeMap, nil
}
