package cluster

import (
	"math/rand/v2"
)

// Leiden runs the Leiden community detection algorithm on g.
//
// gamma controls resolution: higher values produce more, smaller communities.
// Standard modularity uses gamma=1.0. For Go packages, 1.0-2.0 is typical.
//
// seed controls deterministic node ordering for reproducibility.
func Leiden(g *Graph, gamma float64, seed uint64) []int {
	n := g.NumNodes()
	if n == 0 {
		return nil
	}

	communities := make([]int, n)
	for i := range communities {
		communities[i] = i
	}

	rng := rand.New(rand.NewPCG(seed, 0))

	for range 10 {
		improved := localMove(g, communities, gamma, rng)
		if !improved {
			break
		}
		communities = refine(g, communities, gamma, rng)

		agg, mapping := aggregate(g, communities)
		if agg.NumNodes() == n {
			break
		}

		sub := Leiden(agg, gamma, seed)

		// Map aggregated communities back to original nodes.
		for i := range communities {
			communities[i] = sub[mapping[i]]
		}
		n = agg.NumNodes()
	}

	return compact(communities)
}

// localMove visits nodes in random order and greedily moves each to the
// neighboring community that maximizes modularity gain. Repeats until
// no moves improve.
func localMove(g *Graph, communities []int, gamma float64, rng *rand.Rand) bool {
	n := g.NumNodes()
	m2 := 2 * g.TotalWeight
	if m2 == 0 {
		return false
	}

	// Precompute community degree sums.
	commDegree := make(map[int]float64)
	for u := range n {
		commDegree[communities[u]] += g.Degree(u)
	}

	order := rng.Perm(n)
	anyImproved := false

	for changed := true; changed; {
		changed = false
		for _, u := range order {
			ku := g.Degree(u)
			oldComm := communities[u]

			// Remove u from its community for degree computation.
			commDegree[oldComm] -= ku

			// Compute edge weight from u to each neighboring community.
			weightTo := make(map[int]float64)
			for _, e := range g.Adj[u] {
				weightTo[communities[e.Target]] += e.Weight
			}

			bestComm := oldComm
			bestGain := 0.0

			for c, wc := range weightTo {
				// delta_Q = wc/m - gamma * ku * commDegree[c] / (2m^2)
				gain := wc/g.TotalWeight - gamma*ku*commDegree[c]/(m2*m2/2)
				if gain > bestGain {
					bestGain = gain
					bestComm = c
				}
			}

			communities[u] = bestComm
			commDegree[bestComm] += ku

			if bestComm != oldComm {
				changed = true
				anyImproved = true
			}
		}
	}

	return anyImproved
}

// refine splits communities that are not well-connected internally.
// Each community is re-examined: nodes start as singletons within their
// community and are merged only if well-connected to the target subcommunity.
func refine(g *Graph, communities []int, gamma float64, rng *rand.Rand) []int {
	n := g.NumNodes()
	m2 := 2 * g.TotalWeight
	if m2 == 0 {
		return communities
	}

	refined := make([]int, n)
	nextID := 0

	// Group nodes by their community.
	members := make(map[int][]int)
	for u, c := range communities {
		members[c] = append(members[c], u)
	}

	for _, nodes := range members {
		if len(nodes) == 1 {
			refined[nodes[0]] = nextID
			nextID++
			continue
		}

		// Start each node in its own sub-community within this community.
		subComm := make(map[int]int, len(nodes))
		subDegree := make(map[int]float64)
		for _, u := range nodes {
			subComm[u] = nextID
			subDegree[nextID] = g.Degree(u)
			nextID++
		}

		nodeSet := make(map[int]bool, len(nodes))
		for _, u := range nodes {
			nodeSet[u] = true
		}

		// Try merging nodes within this community.
		order := make([]int, len(nodes))
		copy(order, nodes)
		rng.Shuffle(len(order), func(i, j int) {
			order[i], order[j] = order[j], order[i]
		})

		for _, u := range order {
			ku := g.Degree(u)
			oldSub := subComm[u]
			subDegree[oldSub] -= ku

			// Compute edge weight to each sub-community (only within this community).
			weightTo := make(map[int]float64)
			for _, e := range g.Adj[u] {
				if !nodeSet[e.Target] {
					continue
				}
				weightTo[subComm[e.Target]] += e.Weight
			}

			bestSub := oldSub
			bestGain := 0.0

			for sc, wc := range weightTo {
				gain := wc/g.TotalWeight - gamma*ku*subDegree[sc]/(m2*m2/2)
				if gain > bestGain {
					bestGain = gain
					bestSub = sc
				}
			}

			subComm[u] = bestSub
			subDegree[bestSub] += ku
		}

		for _, u := range nodes {
			refined[u] = subComm[u]
		}
	}

	return compact(refined)
}

// aggregate builds a coarsened graph where each community becomes a single node.
// Internal edges within a community become self-loops in the aggregated graph.
// Returns the aggregated graph and a mapping from original node -> aggregated node.
func aggregate(g *Graph, communities []int) (*Graph, []int) {
	// Map community IDs to dense aggregated node IDs.
	commToAgg := make(map[int]int)
	for _, c := range communities {
		if _, ok := commToAgg[c]; !ok {
			commToAgg[c] = len(commToAgg)
		}
	}

	numAgg := len(commToAgg)
	agg := NewGraph(numAgg)
	for i := range agg.Nodes {
		agg.Nodes[i].ID = i
	}

	mapping := make([]int, len(communities))
	for u, c := range communities {
		mapping[u] = commToAgg[c]
	}

	// Accumulate edge weights. Each edge u-v appears in Adj[u] and Adj[v],
	// so we see it twice when iterating all nodes.
	type edgeKey struct{ u, v int }
	interWeights := make(map[edgeKey]float64)
	selfWeights := make([]float64, numAgg)

	for u := range g.Nodes {
		au := mapping[u]
		// Carry over self-loops from the original graph.
		selfWeights[au] += g.SelfLoops[u]
		for _, e := range g.Adj[u] {
			av := mapping[e.Target]
			if au == av {
				// Internal edge becomes self-loop (seen twice, halve later).
				selfWeights[au] += e.Weight
			} else {
				k := edgeKey{min(au, av), max(au, av)}
				interWeights[k] += e.Weight
			}
		}
	}

	// Add self-loops (internal edges seen twice from both endpoints, halve).
	for i, w := range selfWeights {
		if w > 0 {
			agg.AddEdge(i, i, w/2)
		}
	}

	// Add inter-community edges (seen twice from both endpoints, halve).
	for k, w := range interWeights {
		agg.AddEdge(k.u, k.v, w/2)
	}

	return agg, mapping
}

// compact renumbers community IDs to be dense and 0-based.
func compact(communities []int) []int {
	remap := make(map[int]int)
	for _, c := range communities {
		if _, ok := remap[c]; !ok {
			remap[c] = len(remap)
		}
	}
	result := make([]int, len(communities))
	for i, c := range communities {
		result[i] = remap[c]
	}
	return result
}
