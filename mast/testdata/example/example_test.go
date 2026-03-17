package example

import "testing"

// TestServer is a same-package test that references Server.
func TestServer(t *testing.T) {
	var s Server
	s.Addr = "localhost"
	_ = s
}

// testUser is a same-package test helper referencing unexported fields.
func testUser() User {
	return User{Name: "test", Email: "test@test.com", Age: 1}
}
