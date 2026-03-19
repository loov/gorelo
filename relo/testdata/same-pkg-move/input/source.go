package pkg

import "fmt"

type Widget struct {
	Name string
}

func NewWidget(name string) *Widget {
	return &Widget{Name: name}
}

func PrintWidget(w *Widget) {
	fmt.Println(w.Name)
}
