package consumer

import (
	"example.com/crosstest/source"
	"example.com/crosstest/target"
)

func MakeWidget() *target.Widget {
	w := source.NewWidget("test")
	source.PrintWidget(w)
	return w
}
