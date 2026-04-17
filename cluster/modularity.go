package cluster

// Modularity computes the Newman-Girvan modularity Q for the given partition.
//
// Uses the per-community form:
//
//	Q = sum_C [ L_C/m - gamma * (d_C / (2m))^2 ]
//
// where m = total edge weight, L_C = internal edge weight of community C
// (including self-loops), and d_C = sum of degrees of nodes in C.
func Modularity(g *Graph, communities []int, gamma float64) float64 {
	if g.TotalWeight == 0 {
		return 0
	}
	m := g.TotalWeight

	// Compute per-community internal weight and degree sum.
	type commInfo struct {
		internal  float64
		degreeSum float64
	}
	comms := map[int]*commInfo{}

	for u := range g.Nodes {
		c := communities[u]
		ci, ok := comms[c]
		if !ok {
			ci = &commInfo{}
			comms[c] = ci
		}
		ci.degreeSum += g.Degree(u)
		ci.internal += g.SelfLoops[u]
		for _, e := range g.Adj[u] {
			if communities[e.Target] == c {
				ci.internal += e.Weight // counted from both sides, so /2 below
			}
		}
	}

	var q float64
	for _, ci := range comms {
		// Each non-self-loop internal edge is counted twice (once from each endpoint).
		// Self-loops were added once. Normalize to count each edge once.
		lc := ci.internal / 2
		dc := ci.degreeSum
		q += lc/m - gamma*(dc/(2*m))*(dc/(2*m))
	}
	return q
}

// CommunityStat describes a single community in a partition.
type CommunityStat struct {
	ID       int
	Size     int     // number of nodes
	Cohesion float64 // internal edge weight / total incident weight
	Nodes    []int   // node IDs
}

// CommunityStats computes per-community statistics for a partition.
func CommunityStats(g *Graph, communities []int) []CommunityStat {
	members := map[int][]int{}
	for node, comm := range communities {
		members[comm] = append(members[comm], node)
	}

	var stats []CommunityStat
	for commID, nodes := range members {
		nodeSet := make(map[int]bool, len(nodes))
		for _, n := range nodes {
			nodeSet[n] = true
		}

		var internal, total float64
		for _, n := range nodes {
			total += g.Degree(n)
			internal += 2 * g.SelfLoops[n]
			for _, e := range g.Adj[n] {
				if nodeSet[e.Target] {
					internal += e.Weight
				}
			}
		}

		var cohesion float64
		if total > 0 {
			cohesion = internal / total
		}

		stats = append(stats, CommunityStat{
			ID:       commID,
			Size:     len(nodes),
			Cohesion: cohesion,
			Nodes:    nodes,
		})
	}

	return stats
}
