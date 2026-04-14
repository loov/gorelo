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

// Package-level var initialized with a function literal.
// Its parameters live inside a GenDecl rather than a FuncDecl,
// so IsPackageScope must still recognise them as local bindings.
var LogFn = func(page string, err error) {
	_ = page
	_ = err
}
