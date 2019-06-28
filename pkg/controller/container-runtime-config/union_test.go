package containerruntimeconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnionGraph(t *testing.T) {
	for _, c := range []struct {
		name   string
		unions []string // Each string represents as a pair of unionNode values
		result []string // Each string represents a set of unionNode values
	}{
		{name: "Empty", unions: []string{}, result: []string{}},
		{
			name:   "No unions", // With no Union calls, still behaves correctly for individual nodes
			unions: []string{},
			result: []string{"A", "B", "C"},
		},
		{
			name:   "Self-unions",
			unions: []string{"AA", "BB", "CC"},
			result: []string{"A", "B", "C"},
		},
		{
			name:   "Connected: common first element",
			unions: []string{"AB", "AC", "AD", "AE", "ab", "ac", "ad", "ae"},
			result: []string{"ABCDE", "abcde"},
		},
		{
			name:   "Connected: common second element",
			unions: []string{"BA", "CA", "DA", "EA", "ba", "ca", "da", "ea"},
			result: []string{"ABCDE", "abcde"},
		},
		{
			name:   "Connected: path",
			unions: []string{"AB", "BC", "CD", "DE", "ab", "bc", "cd", "de"},
			result: []string{"ABCDE", "abcde"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Verify various rotations of the input to test the behavior is order-independent
			for unionOffset := 0; unionOffset == 0 || unionOffset < len(c.unions); unionOffset++ {
				for resultOffset := 0; resultOffset < len(c.result); resultOffset++ {
					// We could use t.Run() for each offset pair pass, but that would pollute the output too much for developers that
					// want to inspect other tests.
					t.Logf("Pass %d,%d", unionOffset, resultOffset)
					g := newUnionGraph()
					for i := 0; i < len(c.unions); i++ {
						u := c.unions[(unionOffset+i)%len(c.unions)]
						if len(u) != 2 {
							panic("Invalid unions element")
						}
						g.Union(u[0:1], u[1:2])
					}
					for i := 0; i < len(c.result); i++ {
						res := c.result[(resultOffset+i)%len(c.result)]
						expected := g.Find(res[0:1])
						require.Contains(t, res, expected)
						for j := 1; j < len(res); j++ {
							v := g.Find(res[j : j+1])
							assert.Equal(t, expected, v)
						}
						// Check that the root originally returned for res[0:1] did not change by querying the members of the set
						v := g.Find(res[0:1])
						assert.Equal(t, expected, v)
					}
				}
			}
		})
	}
}
