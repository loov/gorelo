package example

// Scope constructs that used to be blind spots for the pattern-matching
// scope predicate. Each binding below must NOT be misclassified as
// package-scope by IsPackageScope — they all live inside a function body,
// a type-switch guard, a composite-literal FuncLit, or a type parameter
// list.

// TypeSwitchGuard: the guard ident `tvGuard` is a local binding that
// lives only in the switch body, not at package scope. A narrowed
// use inside a case body is also local.
func TypeSwitchGuard(v any) int {
	switch tvGuard := v.(type) {
	case int:
		localInt := tvGuard + 1
		return localInt
	case string:
		localStr := len(tvGuard)
		return localStr
	}
	return 0
}

// GenericBody: PG is a type parameter — it is in function scope, not
// package scope. The body also declares a local `pg` of type PG.
// The name is unique to the file so it cannot collide with other
// generic type parameters elsewhere in the package.
func GenericBody[PG any](in PG) PG {
	pg := in
	return pg
}

// MethodValueBind: captures `u.Greet` as a method value assigned to a
// local `greet`. Both `greet` and the method-value use of `Greet`
// occur inside a function body.
type greeter struct{ name string }

func (g *greeter) Greet() string { return "hi, " + g.name }

func MethodValueBind(u *greeter) func() string {
	greet := u.Greet
	return greet
}

// CompositeFuncLit: a slice of function literals. Their parameters
// live inside a GenDecl-style composite literal at statement scope,
// but are still bound inside a function body.
func CompositeFuncLit() []func(int) int {
	return []func(int) int{
		func(part int) int { return part + 1 },
		func(part int) int { return part * 2 },
	}
}

// NestedFuncLits: three levels of func-literal nesting. The innermost
// `step` must still resolve as a local binding, not package-scope.
func NestedFuncLits() func() func() int {
	return func() func() int {
		return func() int {
			step := 1
			return step
		}
	}
}
