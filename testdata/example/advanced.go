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
