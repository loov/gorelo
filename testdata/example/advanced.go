package example

import "fmt"

// Labels
func SearchMatrix(matrix [][]int, target int) (int, int) {
Outer:
	for i, row := range matrix {
		for j, val := range row {
			if val == target {
				return i, j
			}
			if val > target {
				continue Outer
			}
		}
	}
	return -1, -1
}

// Named function type
type Predicate func(User) bool

// Filter uses a named function type as parameter.
func Filter(users []User, pred Predicate) []User {
	var result []User
	for _, u := range users {
		if pred(u) {
			result = append(result, u)
		}
	}
	return result
}

// Closure capturing a variable.
func MakeGreeter(greeting string) func(string) string {
	return func(name string) string {
		return greeting + ", " + name
	}
}

// Named return values.
func Divide(a, b float64) (result float64, err error) {
	if b == 0 {
		err = fmt.Errorf("division by zero")
		return
	}
	result = a / b
	return
}

// Type switch
func Describe(v any) string {
	switch x := v.(type) {
	case User:
		return "user:" + x.Name
	case Counter:
		return fmt.Sprintf("counter:%d", x)
	case Event:
		return "event:" + string(x)
	default:
		return "unknown"
	}
}

// Init function
func init() {
	_ = DefaultUser
}

// Map with named types
type UserIndex map[string]*User

func BuildIndex(users []*User) UserIndex {
	idx := make(UserIndex, len(users))
	for _, u := range users {
		idx[u.Name] = u
	}
	return idx
}

// Blank identifier in range
func CountUsers(users []*User) int {
	count := 0
	for _, _ = range users {
		count++
	}
	return count
}

// Multiple return values used with named types.
func LookupUser(idx UserIndex, name string) (*User, bool) {
	u, ok := idx[name]
	return u, ok
}

// Same-named locals in different functions.
// "name" appears as a parameter in both LookupUser (above) and here.
// "ok" appears as a local in both LookupUser and here.
// They must NOT be in the same group.
func ValidateUser(name string) (bool, error) {
	ok := len(name) > 0
	if !ok {
		return false, fmt.Errorf("empty name")
	}
	return ok, nil
}

// Local variable shadowing a package-level variable.
func ShadowDefaultUser() *User {
	DefaultUser := NewUser("shadow", "shadow@test.com")
	return DefaultUser
}

// Blank identifier usage.
func IgnoreError() {
	_, _ = Divide(1, 0)
}

// Interface method resolution: Stringer.String is defined in types.go,
// User.String is defined in funcs.go. They are separate methods but
// calling s.String() on a Stringer should resolve to Stringer.String.
func CallStringer(s Stringer) string {
	return s.String()
}

// Second init function — Go allows multiple init() per file/package.
// Each is a distinct function and should not merge with the other.
func init() {
	_ = MaxUsers
}

// Closure capturing outer variable: greeting (parameter) is used
// inside the closure and should link to the same group as the def.
// The closure's own "name" parameter must NOT merge with other "name" params.
func MakeCounter(start int) func() int {
	count := start
	return func() int {
		count++
		return count
	}
}

// Short variable declaration reuse: err is reused across := in same scope.
func MultiError() error {
	_, err := Divide(1, 0)
	if err != nil {
		return err
	}
	_, err = Divide(2, 0) // reuse, not :=
	return err
}

// Nested scope: err in the if block is a NEW variable shadowing outer err.
func NestedScopeErr() error {
	var err error
	if true {
		err := fmt.Errorf("inner")
		_ = err
	}
	return err
}

// Method expression: User.String used as a value.
func MethodExpr() func(User) string {
	return User.String
}

// Generic instantiation with named type args.
func MakeCounterPair() Pair[Counter, Counter] {
	return MakePair[Counter, Counter](1, 2)
}

// Interface embedding
type StringerAlt interface {
	Stringer
	Describe() string
}

// Short variable declaration that introduces a new var alongside an existing one.
func ShortVarDeclReuse() (int, error) {
	x, err := Divide(10, 2)
	y, err := Divide(20, 2) // err is reused, y is new
	return int(x + y), err
}

// Receiver variable: "u" is a parameter var on the method.
// It should be scoped to this method, not merged with other "u" params.
func (u *User) Rename(newName string) {
	u.Name = newName
}

// Closure with same-named parameter as outer function.
// Inner "x" should NOT merge with outer "x".
func TransformAll(x int) func(int) int {
	return func(x int) int {
		return x * 2
	}
}

// Pointer method expression: (*User).SetEmail as a value.
func PointerMethodExpr() func(*User, string) {
	return (*User).SetEmail
}

// Type conversion with named type.
func ToCounter(n int) Counter {
	return Counter(n)
}

// Variadic call forwarding.
func FirstName(users ...*User) string {
	names := Names(users...)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// Const in local scope.
func LocalConst() int {
	const limit = 100
	x := limit
	return x
}
