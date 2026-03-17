package example

import "fmt"

// Named types
type Counter int
type Pair[A, B any] struct {
	First  A
	Second B
}

// Type alias
type Number = Counter

// Interface
type Stringer interface{ String() string }

// Interface with same method signature as Stringer
type Alternate interface{ String() string }

// Usage of Stringer interface
func Use(s Stringer) {
	fmt.Println(s.String())
}

// User-defined type constraint.
type Addable interface {
	~int | ~float64
}

// Generic function with user-defined constraint.
type Accumulator[T Addable] struct {
	Total T
}

func (a *Accumulator[T]) Add(v T) {
	a.Total += v
}

// Recursive / self-referencing type.
type Node struct {
	Value    int
	Children []*Node
}

func (n *Node) Depth() int {
	max := 0
	for _, c := range n.Children {
		d := c.Depth()
		if d > max {
			max = d
		}
	}
	return max + 1
}

// Embedded interface in struct.
type Formatted struct {
	Stringer
	Prefix string
}

func FormatWith(f Formatted) string {
	return f.Prefix + f.String()
}
