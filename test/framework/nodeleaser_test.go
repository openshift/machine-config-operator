package framework

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func randomDelay() {
	min := 10
	max := 100
	delay := rand.Intn(max-min+1) + min

	time.Sleep(time.Millisecond * time.Duration(delay))
}

func getNodes() []string {
	nodes := []string{}

	for i := 0; i <= 10; i++ {
		nodes = append(nodes, fmt.Sprintf("node-%d", i))
	}

	return nodes
}

func TestNodeLeaser(t *testing.T) {
	rand.Seed(time.Now().UnixNano())

	testCases := []struct {
		name     string
		testFunc func(*testing.T, *NodeLeaser)
	}{
		{
			name: "Single node lock acquired and released",
			testFunc: func(t *testing.T, nl *NodeLeaser) {
				node := nl.GetNode()
				assert.False(t, nl.nodes[node])
				nl.ReleaseNode(node)
				assert.True(t, nl.nodes[node])
			},
		},
		{
			name: "Multiple node locks acquired and released in single goroutine",
			testFunc: func(t *testing.T, nl *NodeLeaser) {
				for i := 0; i < len(nl.nodes); i++ {
					node := nl.GetNode()
					assert.False(t, nl.nodes[node])
				}

				for nodeName := range nl.nodes {
					nl.ReleaseNode(nodeName)
					assert.True(t, nl.nodes[nodeName])
				}
			},
		},
		{
			name: "Multiple node locks acquired and released across multiple goroutines",
			testFunc: func(t *testing.T, nl *NodeLeaser) {
				wg := sync.WaitGroup{}
				wg.Add(100)

				for i := 0; i <= 100; i++ {
					go func() {
						defer wg.Done()
						node := nl.GetNode()
						assert.NotEmpty(t, node)
						defer nl.ReleaseNode(node)
						randomDelay()
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

			nl := NewNodeLeaser(getNodes())

			testCase.testFunc(t, nl)
		})
	}
}
