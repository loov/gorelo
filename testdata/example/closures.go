package example

// Closure capturing a variable.
func MakeGreeter(greeting string) func(string) string {
	return func(name string) string {
		return greeting + ", " + name
	}
}

// Closure capturing outer variable: the def and all uses
// (including inside the closure) should be in the same group.
func MakeCounter(start int) func() int {
	count := start
	return func() int {
		count++
		return count
	}
}

// Closure with same-named parameter as outer function.
// Inner "x" should NOT merge with outer "x".
func TransformAll(x int) func(int) int {
	return func(x int) int {
		return x * 2
	}
}
