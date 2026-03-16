package example

import "fmt"

// Package-level variables
var DefaultUser = NewUser("default", "default@example.com")

// Typed constant
const MaxUsers Counter = 1000

// Iota constants
type Role int

const (
	RoleGuest Role = iota
	RoleUser
	RoleAdmin
)

// Multiple var declarations
var (
	ErrNotFound = fmt.Errorf("not found")
	ErrDenied   = fmt.Errorf("access denied")
)
