package registries

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopoGraph(t *testing.T) {
	for _, c := range []struct {
		name   string
		edges  []string
		result string
	}{
		// (These test cases contain the same structure in two disconnected components (upper/lower-case); we use topoGraph only
		// for ordering known-connected components, so don't worry about the relative ordering of upper/lower-case nodes, only
		// about the ordering within each component.)
		{name: "Empty", edges: []string{}, result: ""},
		{
			name:   "Shared-from",
			edges:  []string{"AB", "AC", "AD", "ab", "ac", "ad"},
			result: "AaBCDbcd",
		},
		{
			name:   "Shared-to",
			edges:  []string{"AD", "BD", "CD", "ad", "bd", "cd"},
			result: "ABCabcDd",
		},
		{
			name:   "Path",
			edges:  []string{"AB", "BC", "CD", "DE", "ab", "bc", "cd", "de"},
			result: "AaBbCcDdEe",
		},
		{
			name:   "Complete loop",
			edges:  []string{"AB", "BC", "CD", "DA", "ab", "bc", "cd", "da"},
			result: "ABCDabcd",
		},
		{
			name:   "Loop with extra input and output",
			edges:  []string{"AB", "BC", "CD", "DB", "DE", "ab", "bc", "cd", "db", "de"},
			result: "AaBCDEbcde",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			for edgeOffset := 0; edgeOffset == 0 || edgeOffset < len(c.edges); edgeOffset++ {
				// We could use t.Run() for each offset pass, but that would pollute the output too much for developers that
				// want to inspect other tests.
				t.Logf("Pass %d", edgeOffset)
				g := newTopoGraph()
				for i := 0; i < len(c.edges); i++ {
					e := c.edges[(edgeOffset+i)%len(c.edges)]
					require.Len(t, e, 2)
					g.AddEdge(e[0:1], e[1:2])
				}
				res, err := g.Sorted()
				require.NoError(t, err)
				require.Len(t, res, len(c.result))
				for i, v := range res {
					assert.Equal(t, c.result[i:i+1], v)
				}
			}
		})
	}
}
