package example

import "fmt"

// Package-level function
func NewUser(name, email string) *User {
	return &User{Name: name, Email: email}
}

// Method with value receiver
func (u User) String() string {
	return fmt.Sprintf("%s <%s>", u.Name, u.Email)
}

// Method with pointer receiver
func (u *User) SetEmail(email string) {
	u.Email = email
}

// Function using generic type
func MakePair[A, B any](a A, b B) Pair[A, B] {
	return Pair[A, B]{First: a, Second: b}
}

// Variadic function
func Names(users ...*User) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.Name
	}
	return out
}
