package consumer

import "example.com/crosstest/source"

func MakeWidget() *source.Widget {
	w := source.NewWidget("test")
	source.PrintWidget(w)
	return w
}
