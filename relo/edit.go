package relo

// edit represents a text edit: replace bytes [Start, End) with New.
// It is the package-local predecessor of edit.Plan primitives;
// emission sites that still build []edit values lower them to
// primitives via emitSpanRelativeAtAbs (or an inline switch).
type edit struct {
	Start int
	End   int
	New   string
}
