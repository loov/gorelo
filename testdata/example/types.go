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
