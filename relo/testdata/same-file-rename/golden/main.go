package pkg

type Bar struct {
	Value int
}

func NewFoo() *Bar {
	return &Bar{Value: 1}
}

func UseFoo() {
	f := NewFoo()
	_ = f.Value
}
