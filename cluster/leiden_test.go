package cluster

import (
	"testing"
)

func TestLeiden_TwoCliques(t *testing.T) {
	// Two 3-cliques connected by a single edge.
	//   0--1--2  ---  3--4--5
	g := NewGraph(6)
	// Clique A: 0-1, 0-2, 1-2
	g.AddEdge(0, 1, 1)
	g.AddEdge(0, 2, 1)
	g.AddEdge(1, 2, 1)
	// Clique B: 3-4, 3-5, 4-5
	g.AddEdge(3, 4, 1)
	g.AddEdge(3, 5, 1)
	g.AddEdge(4, 5, 1)
	// Bridge: 2-3
	g.AddEdge(2, 3, 1)

	communities := Leiden(g, 1.0, 42)

	// Nodes 0,1,2 should be in the same community.
	if communities[0] != communities[1] || communities[1] != communities[2] {
		t.Errorf("clique A not together: %v", communities)
	}
	// Nodes 3,4,5 should be in the same community.
	if communities[3] != communities[4] || communities[4] != communities[5] {
		t.Errorf("clique B not together: %v", communities)
	}
	// The two cliques should be in different communities.
	if communities[0] == communities[3] {
		t.Errorf("cliques not separated: %v", communities)
	}
}

func TestLeiden_SingleClique(t *testing.T) {
	// Complete graph K4 -> should be one community.
	g := NewGraph(4)
	g.AddEdge(0, 1, 1)
	g.AddEdge(0, 2, 1)
	g.AddEdge(0, 3, 1)
	g.AddEdge(1, 2, 1)
	g.AddEdge(1, 3, 1)
	g.AddEdge(2, 3, 1)

	communities := Leiden(g, 1.0, 42)

	for i := 1; i < 4; i++ {
		if communities[i] != communities[0] {
			t.Errorf("K4 split into multiple communities: %v", communities)
			break
		}
	}
}

func TestLeiden_Disconnected(t *testing.T) {
	// Two disconnected pairs: {0,1} and {2,3}.
	g := NewGraph(4)
	g.AddEdge(0, 1, 1)
	g.AddEdge(2, 3, 1)

	communities := Leiden(g, 1.0, 42)

	if communities[0] != communities[1] {
		t.Errorf("pair {0,1} split: %v", communities)
	}
	if communities[2] != communities[3] {
		t.Errorf("pair {2,3} split: %v", communities)
	}
	if communities[0] == communities[2] {
		t.Errorf("disconnected components merged: %v", communities)
	}
}

func TestLeiden_SingleNode(t *testing.T) {
	g := NewGraph(1)
	communities := Leiden(g, 1.0, 42)
	if len(communities) != 1 || communities[0] != 0 {
		t.Errorf("unexpected: %v", communities)
	}
}

func TestLeiden_Empty(t *testing.T) {
	g := NewGraph(0)
	communities := Leiden(g, 1.0, 42)
	if communities != nil {
		t.Errorf("expected nil for empty graph, got %v", communities)
	}
}

func TestLeiden_GammaSensitivity(t *testing.T) {
	// Ring of 6 nodes with three pairs more strongly connected.
	// 0=1  2=3  4=5  (strong), 1-2, 3-4, 5-0 (weak).
	g := NewGraph(6)
	g.AddEdge(0, 1, 5)
	g.AddEdge(2, 3, 5)
	g.AddEdge(4, 5, 5)
	g.AddEdge(1, 2, 1)
	g.AddEdge(3, 4, 1)
	g.AddEdge(5, 0, 1)

	lowGamma := Leiden(g, 0.5, 42)
	highGamma := Leiden(g, 3.0, 42)

	lowCount := countCommunities(lowGamma)
	highCount := countCommunities(highGamma)

	if highCount < lowCount {
		t.Errorf("higher gamma should produce >= communities: low=%d (gamma=0.5), high=%d (gamma=3.0)",
			lowCount, highCount)
	}
}

func countCommunities(communities []int) int {
	seen := map[int]bool{}
	for _, c := range communities {
		seen[c] = true
	}
	return len(seen)
}
