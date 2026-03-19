package pkg

type Foo struct {
	Value int
}

func NewFoo() *Foo {
	return &Foo{Value: 1}
}

func UseFoo() {
	f := NewFoo()
	_ = f.Value
}
