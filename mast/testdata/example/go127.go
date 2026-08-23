package example

// Go 1.27 language features: generic methods and promoted-field keys
// in composite literals.

// GList is a generic type with generic methods.
type GList[E any] []E

// Apply is a generic method; GM is a method-level type parameter.
func (l GList[E]) Apply[GM any](f func(E) GM) GList[GM] {
	r := make(GList[GM], len(l))
	for i, x := range l {
		r[i] = f(x)
	}
	return r
}

// Collect is another generic method reusing the name GM.
func (l GList[E]) Collect[GM any](f func(E) GM) []GM {
	return l.Apply[GM](f)
}

// PromotedBase is embedded by PromotedOuter.
type PromotedBase struct {
	PromotedName string
}

// PromotedOuter embeds PromotedBase.
type PromotedOuter struct {
	PromotedBase
	Extra int
}

// NewPromotedOuter uses a promoted field as a composite literal key.
func NewPromotedOuter() PromotedOuter {
	return PromotedOuter{PromotedName: "x", Extra: 1}
}
