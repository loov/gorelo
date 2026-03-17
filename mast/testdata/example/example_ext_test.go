package example_test

import (
	"testing"

	"example"
)

// TestCounter is an external test that references exported types.
func TestCounter(t *testing.T) {
	var c example.Counter
	_ = c
}

// TestAdmin tests promoted field access from external test.
func TestAdmin(t *testing.T) {
	a := example.Admin{}
	_ = a.Name
}
