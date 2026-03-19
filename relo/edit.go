package relo

import (
	"fmt"
	"sort"
)

// edit represents a text edit: replace bytes [Start, End) with New.
type edit struct {
	Start int
	End   int
	New   string
}

// deduplicateEdits removes duplicate edits at the same Start offset.
// It panics if two edits share the same Start but differ in End or New.
func deduplicateEdits(edits []edit) []edit {
	if len(edits) == 0 {
		return edits
	}
	seen := make(map[int]edit)
	out := edits[:0:0]
	for _, e := range edits {
		if prev, ok := seen[e.Start]; ok {
			if prev.End != e.End || prev.New != e.New {
				panic(fmt.Sprintf("deduplicateEdits: conflicting edits at offset %d: {End:%d, New:%q} vs {End:%d, New:%q}",
					e.Start, prev.End, prev.New, e.End, e.New))
			}
			continue
		}
		seen[e.Start] = e
		out = append(out, e)
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
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Start < sorted[j].Start
	})
	var b []byte
	pos := 0
	for _, e := range sorted {
		if e.Start < 0 || e.End < e.Start || e.Start > len(src) || e.End > len(src) {
			panic(fmt.Sprintf("applyEdits: edit out of bounds: edit{Start:%d, End:%d, New:%q} with src length %d", e.Start, e.End, e.New, len(src)))
		}
		// Overlapping edits are intentionally skipped rather than treated as
		// errors.  This can happen legitimately when a rename edit and a
		// self-import removal edit share a boundary, or when deduplication
		// leaves partially overlapping ranges.  The first (lower-Start) edit
		// wins and later overlapping edits are silently dropped.
		if e.Start < pos {
			continue
		}
		b = append(b, src[pos:e.Start]...)
		b = append(b, e.New...)
		pos = e.End
	}
	b = append(b, src[pos:]...)
	return string(b)
}
