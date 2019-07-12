package containerruntimeconfig

// unionNode is a node in the union graph. In our case, it is a mirror host[:port].
type unionNode = string // An alias, so that the users don't have to explicitly cast data.

// internalUnionNode is the unionGraph representation of a node.
// There is exactly one instance of *internalUnionNode for each .public value.
type internalUnionNode struct {
	public unionNode
	parent *internalUnionNode // A higher-ranking member of the same disjoint set, or nil
	rank   int
}

// unionGraph is a set of unionNodes which supports identifying disjoint subsets.
// See e.g. https://en.wikipedia.org/wiki/Disjoint-set_data_structure .
// The graph is built implicitly, i.e. there is no explicit “add node” operation; it is perfectly valid
// to just call .Union or .Find using unionNode values that have never been mentioned before.
type unionGraph struct {
	// This is not all that efficient, the hash map lookups by unionNode == string are likely more costly than any other
	// use of the graph, but the graphs we need to handle are very small and readability is more important.
	nodes map[unionNode]*internalUnionNode
}

// newUnionGraph returns an empty unionGraph.
func newUnionGraph() *unionGraph {
	return &unionGraph{
		nodes: map[unionNode]*internalUnionNode{},
	}
}

// getNode returns g.nodes[node], creating it if it does not exist yet.
func (g *unionGraph) getNode(node unionNode) *internalUnionNode {
	res, ok := g.nodes[node]
	if !ok {
		res = &internalUnionNode{
			public: node,
			parent: nil,
			rank:   0,
		}
		g.nodes[node] = res
	}
	return res
}

// find is Find(), except it returns a *internalUnionNode
func (g *unionGraph) find(query unionNode) *internalUnionNode {
	queryNode := g.getNode(query)
	// Find root
	x := queryNode
	for x.parent != nil {
		x = x.parent
	}
	root := x
	// Compress path
	x = queryNode
	for x != root {
		next := x.parent
		x.parent = root
		x = next
	}
	return root
}

// Find returns a representative member of the disjoint set containing query.
// All Find() calls on members of the same disjoint set return the same representative member until g.Union is called again.
func (g *unionGraph) Find(query unionNode) unionNode {
	return g.find(query).public
}

// Union joins the sets of a, b
func (g *unionGraph) Union(a, b unionNode) {
	inA := g.find(a)
	inB := g.find(b)
	if inA != inB {
		if inA.rank > inB.rank {
			inB.parent = inA
		} else if inA.rank < inB.rank {
			inA.parent = inB
		} else { // inA.rank == inB.rank
			inA.parent = inB
			inB.rank++
		}
	}
}
