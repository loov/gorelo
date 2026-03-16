package example

// Regular struct with fields
type User struct {
	Name  string
	Email string
	Age   int
}

// Embedded field
type Admin struct {
	User           // embedded — renaming User type should rename this too
	Permissions []string
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
