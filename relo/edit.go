package relo

import (
	"sort"
)

// edit represents a text edit: replace bytes [Start, End) with New.
type edit struct {
	Start int
	End   int
	New   string
}

// deduplicateEdits removes duplicate edits at the same Start offset.
func deduplicateEdits(edits []edit) []edit {
	if len(edits) == 0 {
		return edits
	}
	seen := make(map[int]bool)
	out := edits[:0:0]
	for _, e := range edits {
		if !seen[e.Start] {
			seen[e.Start] = true
			out = append(out, e)
		}
	}
	return out
}

// applyEditsToString is like applyEdits but accepts a string source.
func applyEditsToString(src string, edits []edit) string {
	return applyEdits([]byte(src), edits)
}

// applyEdits applies non-overlapping edits to src, returning the result.
func applyEdits(src []byte, edits []edit) string {
	if len(edits) == 0 {
		return string(src)
	}
	sorted := make([]edit, len(edits))
	copy(sorted, edits)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Start < sorted[j].Start
	})
	var b []byte
	pos := 0
	for _, e := range sorted {
		if e.Start < pos {
			continue // skip overlapping
		}
		b = append(b, src[pos:e.Start]...)
		b = append(b, e.New...)
		pos = e.End
	}
	b = append(b, src[pos:]...)
	return string(b)
}
