package pkg

import "fmt"

func NewWidget(name string) *Widget {
	return &Widget{Name: name}
}

func PrintWidget(w *Widget) {
	fmt.Println(w.Name)
}
