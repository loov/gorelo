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

// Map with named types
type UserIndex map[string]*User

func BuildIndex(users []*User) UserIndex {
	idx := make(UserIndex, len(users))
	for _, u := range users {
		idx[u.Name] = u
	}
	return idx
}

// Multiple return values used with named types.
func LookupUser(idx UserIndex, name string) (*User, bool) {
	u, ok := idx[name]
	return u, ok
}

// Interface method call through interface value.
func CallStringer(s Stringer) string {
	return s.String()
}

// Method expression: User.String used as a value.
func MethodExpr() func(User) string {
	return User.String
}

// Pointer method expression: (*User).SetEmail as a value.
func PointerMethodExpr() func(*User, string) {
	return (*User).SetEmail
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

// Defer with method call.
func DeferPrint(s *Server) {
	defer s.Print()
	fmt.Println("before print")
}

// Go with function call.
func GoProducer(ch chan<- Event) {
	go Producer(ch, "a", "b")
}

// Generic struct field access.
func PairFirst(p Pair[Counter, Counter]) Counter {
	return p.First
}

// Slice with named element type.
type CounterSlice []Counter

func SumCounters(cs CounterSlice) Counter {
	var total Counter
	for _, c := range cs {
		total += c
	}
	return total
}

// Pointer to named type in function signature.
func IncrementCounter(c *Counter) {
	*c++
}

// Switch case with variable scoping.
func SwitchScope(v any) string {
	switch x := v.(type) {
	case User:
		name := x.Name
		return name
	case Counter:
		name := fmt.Sprintf("%d", x)
		return name
	}
	return ""
}

// Composite literal with nested struct.
func NewServer(addr string) Server {
	return Server{
		Addr: addr,
		TLS: struct {
			CertFile string
			KeyFile  string
		}{
			CertFile: "/etc/cert.pem",
			KeyFile:  "/etc/key.pem",
		},
	}
}
