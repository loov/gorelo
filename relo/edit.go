package relo

// editSet collects FileEdits keyed by path, handling deduplication
// when a file is both a target and a source.
type editSet struct {
	edits []FileEdit
	index map[string]int // path -> index in edits
}

func newEditSet() *editSet {
	return &editSet{index: make(map[string]int)}
}

// Set creates or replaces the edit for path with the given content.
func (es *editSet) Set(fe FileEdit) {
	if idx, ok := es.index[fe.Path]; ok {
		es.edits[idx] = fe
		return
	}
	es.index[fe.Path] = len(es.edits)
	es.edits = append(es.edits, fe)
}

// Get returns the current content for path, or ("", false) if not present or deleted.
func (es *editSet) Get(path string) (string, bool) {
	idx, ok := es.index[path]
	if !ok || es.edits[idx].IsDelete {
		return "", false
	}
	return es.edits[idx].Content, true
}

// Has reports whether an edit exists for path.
func (es *editSet) Has(path string) bool {
	_, ok := es.index[path]
	return ok
}

// Edits returns the collected edits.
func (es *editSet) Edits() []FileEdit {
	return es.edits
}

// edit represents a text edit: replace bytes [Start, End) with New.
// It is the package-local predecessor of edit.Plan primitives; emission
// sites still build []edit values that get lowered to a sub-Plan before
// application via applyEditsViaPlan.
type edit struct {
	Start int
	End   int
	New   string
}
