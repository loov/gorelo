package example

import "fmt"

// Regular struct with fields
type User struct {
	Name  string
	Email string
	Age   int
}

// Embedded field
type Admin struct {
	User        // embedded — renaming User type should rename this too
	Permissions []string
}

// Type alias for struct
type Member = User

// Field access through type alias
func MemberName(m Member) string {
	return m.Name
}

// Anonymous struct in var
var Config = struct {
	Host string
	Port int
}{
	Host: "localhost",
	Port: 8080,
}

// Nested anonymous struct as field type
type Server struct {
	Addr string
	TLS  struct {
		CertFile string
		KeyFile  string
	}
}

// Method on Server struct using anonymous fields.
func (server *Server) Print() {
	fmt.Println(server.Addr)
	fmt.Println(server.TLS.CertFile)
	fmt.Println(server.TLS.KeyFile)
}

// Multi-level embedding: SuperAdmin → Admin → User
type SuperAdmin struct {
	Admin
	Level int
}

// Promoted field access through two levels of embedding.
func SuperAdminName(sa SuperAdmin) string {
	return sa.Name
}

// Same-named method on different type than User.String().
func (s *Server) String() string {
	return s.Addr
}

// Selector on return value.
func DefaultUserName() string {
	return NewUser("test", "test@test.com").Name
}

// Second anonymous struct with a same-named field as Server.TLS
// to test anonymous struct field key collision.
type Database struct {
	Addr string
	TLS  struct {
		CertFile string
		CAFile   string
	}
}

func (db *Database) PrintTLS() {
	fmt.Println(db.TLS.CertFile)
	fmt.Println(db.TLS.CAFile)
}

// Function on package level using anonymous fields.
func PrintConfig() {
	fmt.Println(Config.Host)
	fmt.Println(Config.Port)
}
