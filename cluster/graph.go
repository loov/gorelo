package cluster

// Graph is an undirected weighted graph for community detection.
// Nodes are identified by dense integer IDs (0-based).
// Self-loops are tracked separately and represent internal edge weight
// in aggregated graphs.
type Graph struct {
	Nodes       []Node
	Adj         [][]Edge  // Adj[i] = edges from node i (excludes self-loops)
	SelfLoops   []float64 // self-loop weight per node
	TotalWeight float64   // sum of all edge weights including self-loops (each counted once)
}

// Node is a graph vertex with display metadata.
type Node struct {
	ID    int
	Label string // declaration name, e.g. "Server"
	Kind  string // "type", "func", "var", "const"
	File  string // relative file path of definition
	Line  int    // definition line number
}

// Edge is a weighted directed edge (graphs store both directions for undirected).
type Edge struct {
	Target int
	Weight float64
}

// NewGraph creates a graph with n nodes. Caller fills in Node metadata
// and adds edges via AddEdge.
func NewGraph(n int) *Graph {
	return &Graph{
		Nodes:     make([]Node, n),
		Adj:       make([][]Edge, n),
		SelfLoops: make([]float64, n),
	}
}

// AddEdge adds an undirected weighted edge between u and v.
// If an edge already exists, the weight is accumulated.
func (g *Graph) AddEdge(u, v int, weight float64) {
	if u == v {
		g.SelfLoops[u] += weight
		g.TotalWeight += weight
		return
	}
	g.addDirected(u, v, weight)
	g.addDirected(v, u, weight)
	g.TotalWeight += weight
}

func (g *Graph) addDirected(from, to int, weight float64) {
	for i := range g.Adj[from] {
		if g.Adj[from][i].Target == to {
			g.Adj[from][i].Weight += weight
			return
		}
	}
	g.Adj[from] = append(g.Adj[from], Edge{Target: to, Weight: weight})
}

// Degree returns the weighted degree of node u.
// Self-loops contribute twice to degree (both endpoints are u).
func (g *Graph) Degree(u int) float64 {
	var d float64
	for _, e := range g.Adj[u] {
		d += e.Weight
	}
	d += 2 * g.SelfLoops[u]
	return d
}

// NumNodes returns the number of nodes.
func (g *Graph) NumNodes() int { return len(g.Nodes) }
