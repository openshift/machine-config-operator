package framework

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

func randomDelay() {
	min := 10
	max := 100
	delay := rand.Intn(max-min+1) + min

	time.Sleep(time.Millisecond * time.Duration(delay))
}

func getNodes(num int, role string) []runtime.Object {
	nodes := []runtime.Object{}

	for i := 0; i <= num; i++ {
		name := fmt.Sprintf("%s-node-%d", role, i)
		nodes = append(nodes, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					fmt.Sprintf("%s/%s", nodeRoleLabelKey, role): "",
				},
			},
		})
		nodes = append(nodes)
	}

	return nodes
}

func TestNodeLeaser(t *testing.T) {
	rand.Seed(time.Now().UnixNano())

	testCases := []struct {
		name     string
		testFunc func(*testing.T, corev1client.CoreV1Interface, *NodeLeaser)
	}{
		{
			name: "Single node lock acquired and released",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				node, releaseFunc, err := nl.GetNodeWithReleaseFunc(t)
				assert.NoError(t, err)
				assert.False(t, nl.nodes[node.Name])
				releaseFunc()
				assert.True(t, nl.nodes[node.Name])

				node, err = nl.GetNode(t)
				assert.NoError(t, err)
				assert.False(t, nl.nodes[node.Name])
				assert.NoError(t, nl.ReleaseNode(t, node))
				assert.True(t, nl.nodes[node.Name])

				// Release unknown node
				assert.Error(t, nl.ReleaseNode(t, &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "unknown-node",
					},
				}))
			},
		},
		{
			name: "Multiple node locks acquired and released in single goroutine",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				for i := 0; i < len(nl.nodes); i++ {
					node, err := nl.GetNode(t)
					assert.NoError(t, err)
					assert.False(t, nl.nodes[node.Name])
				}

				for nodeName := range nl.nodes {
					nl.ReleaseNode(t, &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Name: nodeName,
						},
					})
					assert.True(t, nl.nodes[nodeName])
				}
			},
		},
		{
			name: "Test does not remove additional role label",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				node, err := nl.GetNode(t)
				assert.NoError(t, err)

				otherRole := addNodeRoleLabelKey("other-role")

				node.Labels[otherRole] = ""
				_, err = client.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
				require.NoError(t, err)

				assert.Error(t, nl.ReleaseNode(t, node))

				// If a node has another role and it was never released, we should get
				// an error upon attempted retrieval.
				allNodes, err := client.Nodes().List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, err)

				for _, node := range allNodes.Items {
					node := node
					node.Labels[otherRole] = ""
					_, err = client.Nodes().Update(context.TODO(), &node, metav1.UpdateOptions{})
					require.NoError(t, err)
				}

				_, err = nl.GetNode(t)
				assert.Error(t, err)
			},
		},
		{
			name: "Multiple node locks acquired and released across multiple goroutines",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				wg := sync.WaitGroup{}
				wg.Add(100)

				for i := 0; i < 100; i++ {
					go func() {
						defer wg.Done()
						node, releaseFunc, err := nl.GetNodeWithReleaseFunc(t)
						assert.NoError(t, err)
						assert.NotNil(t, node)
						randomDelay()
						releaseFunc()
					}()
				}

				wg.Wait()
			},
		},
		{
			name: "Multiple node locks acquired and released across multiple goroutines",
			testFunc: func(t *testing.T, client corev1client.CoreV1Interface, nl *NodeLeaser) {
				wg := sync.WaitGroup{}
				wg.Add(100)

				for i := 0; i < 100; i++ {
					i := i
					go func() {
						defer wg.Done()
						node, releaseFunc, err := nl.GetNodeWithReleaseFunc(t)
						assert.NoError(t, err)
						assert.NotNil(t, node)

						otherrole := addNodeRoleLabelKey("other-role-" + fmt.Sprint(i))
						node.Labels[otherrole] = ""

						_, err = client.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
						require.NoError(t, err)

						randomDelay()

						delete(node.Labels, otherrole)

						_, err = client.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
						require.NoError(t, err)

						releaseFunc()
					}()
				}

				wg.Wait()
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			nodes := getNodes(10, "worker")

			client := fakeclient.NewSimpleClientset(nodes...).CoreV1()

			nl, err := NewNodeLeaser(client, "worker")
			assert.NoError(t, err)

			testCase.testFunc(t, client, nl)
		})
	}
}
