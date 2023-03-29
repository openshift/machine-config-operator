package framework

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
}

// Returns a new NodeLeaser
func NewNodeLeaser(nodes []string) *NodeLeaser {
	nodeMap := map[string]bool{}
	for _, node := range nodes {
		nodeMap[node] = true
	}

	return &NodeLeaser{
		nodes: nodeMap,
		mux:   &sync.Mutex{},
	}
}

func (n *NodeLeaser) GetNode(t *testing.T, cs *ClientSet) (*corev1.Node, func(), error) {
	now := time.Now()
	nodeName := n.GetNodeName()
	t.Logf("Waited for node %s for %s", nodeName, time.Since(now))

	node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), n.GetNodeName(), metav1.GetOptions{})

	now = time.Now()

	releaseFunc := func() {
		n.ReleaseNode(t, node)
		t.Logf("Node %s released; test run time: %s", node.Name, time.Since(now))
	}

	return node, releaseFunc, err
}

// Acquires the first available node. If no nodes are available, this will
// block until a node becomse available.
func (n *NodeLeaser) GetNodeName() string {
	for {
		node, found := n.findFreeNode()
		if found {
			return node
		}
		// Sleep so we don't use a ton of CPU.
		time.Sleep(time.Millisecond)
	}
}

func (n *NodeLeaser) ReleaseNode(t *testing.T, node *corev1.Node) {
	t.Logf("Released node %s", node.Name)
	n.ReleaseNodeName(node.Name)
}

// Releases the provided node.
func (n *NodeLeaser) ReleaseNodeName(node string) {
	n.mux.Lock()
	defer n.mux.Unlock()
	n.nodes[node] = true
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
