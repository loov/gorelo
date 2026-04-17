package cluster

import (
	"math"
	"testing"
)

func TestModularity_PerfectPartition(t *testing.T) {
	// Two disconnected pairs — perfect partition should have positive modularity.
	g := NewGraph(4)
	g.AddEdge(0, 1, 1)
	g.AddEdge(2, 3, 1)

	good := Modularity(g, []int{0, 0, 1, 1}, 1.0)
	bad := Modularity(g, []int{0, 1, 0, 1}, 1.0)

	if good <= 0 {
		t.Errorf("perfect partition modularity should be positive, got %f", good)
	}
	if good <= bad {
		t.Errorf("perfect partition (%f) should beat bad partition (%f)", good, bad)
	}
}

func TestModularity_SingleCommunity(t *testing.T) {
	// All nodes in one community -> modularity = 0.
	g := NewGraph(3)
	g.AddEdge(0, 1, 1)
	g.AddEdge(1, 2, 1)

	q := Modularity(g, []int{0, 0, 0}, 1.0)
	if math.Abs(q) > 1e-10 {
		t.Errorf("single community modularity should be ~0, got %f", q)
	}
}

func TestModularity_EmptyGraph(t *testing.T) {
	g := NewGraph(2)
	q := Modularity(g, []int{0, 1}, 1.0)
	if q != 0 {
		t.Errorf("empty graph modularity should be 0, got %f", q)
	}
}

func TestCommunityStats(t *testing.T) {
	g := NewGraph(4)
	g.AddEdge(0, 1, 3)
	g.AddEdge(2, 3, 2)
	g.AddEdge(1, 2, 1) // cross-community edge

	stats := CommunityStats(g, []int{0, 0, 1, 1})

	if len(stats) != 2 {
		t.Fatalf("expected 2 communities, got %d", len(stats))
	}

	for _, st := range stats {
		if st.Size != 2 {
			t.Errorf("community %d: expected size 2, got %d", st.ID, st.Size)
		}
		if st.Cohesion <= 0 || st.Cohesion > 1 {
			t.Errorf("community %d: cohesion %f out of range (0,1]", st.ID, st.Cohesion)
		}
	}
}
