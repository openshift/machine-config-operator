package framework

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

func TestNodeLeaser(t *testing.T) {
	rand.Seed(time.Now().UnixNano())

	testCases := []struct {
		name     string
		testFunc func(*testing.T, corev1client.CoreV1Interface, *NodeLeaser)
	}{
		{
			name: "Single node lock acquired and released",
			testFunc: func(t *testing.T, _ corev1client.CoreV1Interface, nl *NodeLeaser) {
				t.Run("ReleaseNode", func(t *testing.T) {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.False(t, nl.nodes[node.Name])
					assert.Nil(t, nl.ReleaseNode(t, node))
					assert.True(t, nl.nodes[node.Name])
				})

				t.Run("ReleaseFunc", func(t *testing.T) {
					node, releaseFunc, err := nl.GetNodeWithReleaseFunc(t)
					assert.NoError(t, err)
					assert.False(t, nl.nodes[node.Name])
					releaseFunc()
					assert.True(t, nl.nodes[node.Name])
				})
			},
		},
		{
			name: "Release unknown node",
			testFunc: func(t *testing.T, _ corev1client.CoreV1Interface, nl *NodeLeaser) {
				err := nl.ReleaseNode(t, newNode("unknown-node"))
				assert.Error(t, err)
			},
		},
		{
			// Note: We don't use the releaseFunc mechanism in these tests because it
			// will cause the test suite to fail.
			name: "Node cannot be released",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				t.Run("Has additional role", func(t *testing.T) {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)

					node.Labels[addNodeRoleLabelKey("additional-role")] = ""
					updateNode(t, client, node)
					assert.Error(t, nl.ReleaseNode(t, node))
				})

				t.Run("Has MachineConfig mismatch", func(*testing.T) {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)

					node.Annotations[constants.CurrentMachineConfigAnnotationKey] = "abc"
					node.Annotations[constants.DesiredMachineConfigAnnotationKey] = "123"
					updateNode(t, client, node)
					assert.Error(t, nl.ReleaseNode(t, node))
				})

				t.Run("Is degraded", func(t *testing.T) {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)

					node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] = constants.MachineConfigDaemonStateDegraded
					updateNode(t, client, node)
					assert.Error(t, nl.ReleaseNode(t, node))
				})
			},
		},
		{
			name: "Multiple node locks acquired and released in single goroutine",
			testFunc: func(t *testing.T, _ corev1client.CoreV1Interface, nl *NodeLeaser) {
				allocatedNodes := []*corev1.Node{}

				for i := 0; i < len(nl.nodes); i++ {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)
					assert.False(t, nl.nodes[node.Name])
					allocatedNodes = append(allocatedNodes, node)
				}

				assert.Len(t, nl.nodes, 10)

				for _, node := range allocatedNodes {
					nl.ReleaseNode(t, node)
					assert.True(t, nl.nodes[node.Name])
				}
			},
		},
		{
			name: "Multiple node locks acquired and released across multiple goroutines",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				wg := sync.WaitGroup{}

				for i := 0; i <= 100; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						node, err := nl.GetNode(t)
						assert.NoError(t, err)
						assert.NotNil(t, node)

						node.Labels[addNodeRoleLabelKey("test-role")] = ""
						updateNode(t, client, node)
						defer func() {
							assert.NoError(t, nl.ReleaseNode(t, node))
						}()

						delete(node.Labels, addNodeRoleLabelKey("test-role"))
						updateNode(t, client, node)
					}()
				}

				wg.Wait()

				for node := range nl.nodes {
					assert.True(t, nl.nodes[node], "expected node %s to be freed", node)
				}
			},
		},
		{
			name: "Node deleted from cluster",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				node, err := nl.GetNode(t)
				assert.NoError(t, err)
				assert.NotNil(t, node)

				require.NoError(t, client.Nodes().Delete(context.TODO(), node.Name, metav1.DeleteOptions{}))

				assert.NoError(t, nl.ReleaseNode(t, node))
				assert.NotContains(t, nl.nodes, node.Name)
			},
		},
		{
			name: "Node deleted before use",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				allocatedNodes := []*corev1.Node{}

				// Allocate all nodes except for one.
				for i := 0; i <= len(nl.nodes)-2; i++ {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)
					allocatedNodes = append(allocatedNodes, node)
				}

				// Select a random node to release.
				nodeToRelease := allocatedNodes[rand.Intn(len(allocatedNodes))]

				// Find an unallocated node to delete.
				nodeToDelete, _ := nl.findFreeNode()

				// Ensure that we actually found a node.
				assert.NotEmpty(t, nodeToDelete)

				// Delete the unallocated node from the API server.
				require.NotEmpty(t, nodeToDelete)
				require.NoError(t, client.Nodes().Delete(context.TODO(), nodeToDelete, metav1.DeleteOptions{}))

				// Ensure that our node cache still contains the node name and that it is unallocated.
				assert.Contains(t, nl.nodes, nodeToDelete)
				assert.True(t, nl.nodes[nodeToDelete])

				// Ensure that the API server does not know about the node.
				_, err := client.Nodes().Get(context.TODO(), nodeToDelete, metav1.GetOptions{})
				require.Error(t, err)

				t.Log(nodeToRelease)

				// Spawn a Goroutine so that we do not block while we wait for the next
				// available node.
				waitChan := make(chan struct{})
				go func() {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)

					assert.Equal(t, nodeToRelease.Name, node.Name)
					close(waitChan)
				}()

				// Release the node we've previously selected.
				assert.NoError(t, nl.ReleaseNode(t, nodeToRelease))

				// Wait for the Goroutine to shut down.
				<-waitChan
			},
		},
		{
			name: "Node added to cluster",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				insertNode := func(t *testing.T, n *corev1.Node) {
					_, err := client.Nodes().Create(context.TODO(), n, metav1.CreateOptions{})
					require.NoError(t, err)
				}

				t.Run("Is ready", func(t *testing.T) {
					addedNode := newNode("new-ready-node")

					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)

					insertNode(t, addedNode)

					assert.NoError(t, nl.ReleaseNode(t, node))
					assert.Contains(t, nl.nodes, addedNode.Name)
					assert.True(t, nl.nodes[addedNode.Name])
				})

				t.Run("Is not ready", func(t *testing.T) {
					addedNode := newNode("new-unready-node")
					addedNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey] = constants.MachineConfigDaemonStateWorking

					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.NotNil(t, node)

					insertNode(t, addedNode)

					assert.NoError(t, nl.ReleaseNode(t, node))
					assert.Contains(t, nl.nodes, addedNode.Name)
					assert.False(t, nl.nodes[addedNode.Name])
				})
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			client := fakeclient.NewSimpleClientset(getNodes()...).CoreV1()

			nl, err := NewNodeLeaser(client, "worker")
			assert.NoError(t, err)

			testCase.testFunc(t, client, nl)
		})
	}
}

func newNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				addNodeRoleLabelKey("worker"): "",
			},
			Annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "",
				constants.DesiredMachineConfigAnnotationKey:     "",
				constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
			},
		},
	}
}

func getNodes() []runtime.Object {
	nodes := []runtime.Object{}

	for i := 0; i < 10; i++ {
		nodes = append(nodes, newNode(fmt.Sprintf("node-%d", i)))
	}

	return nodes
}

func updateNode(t *testing.T, client corev1client.CoreV1Interface, node *corev1.Node) {
	t.Helper()

	updatedNode, err := client.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	updatedNode.Labels = node.Labels
	updatedNode.Annotations = node.Annotations

	_, err = client.Nodes().Update(context.TODO(), updatedNode, metav1.UpdateOptions{})
	require.NoError(t, err)
}
