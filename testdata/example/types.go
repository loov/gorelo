package example

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
