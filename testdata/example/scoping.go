package example

import "fmt"

// Same-named locals in different functions.
// "name" appears as a parameter in both LookupUser and here.
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

// Init function
func init() {
	_ = DefaultUser
}

// Second init function — Go allows multiple init() per file/package.
// Each is a distinct function and should not merge with the other.
func init() {
	_ = MaxUsers
}

// Blank identifier in range
func CountUsers(users []*User) int {
	count := 0
	for _, _ = range users {
		count++
	}
	return count
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

// Const in local scope.
func LocalConst() int {
	const limit = 100
	x := limit
	return x
}
