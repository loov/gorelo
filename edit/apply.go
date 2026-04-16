package edit

import (
	"errors"
	"fmt"
	"sort"
)

// Apply applies the plan to files and returns the resulting contents.
//
// files is a map from file path to original byte contents. Paths not
// present in files but referenced by a primitive are treated as if
// their original content were empty.
//
// On any composition conflict Apply returns a *ConflictError; on a
// bounds error or other structural problem it returns a plain error.
func (p *Plan) Apply(files map[string][]byte) (map[string][]byte, error) {
	byFile := make(map[string][]Primitive)
	for _, prim := range p.prims {
		switch x := prim.(type) {
		case Insert:
			byFile[x.Anchor.Path] = append(byFile[x.Anchor.Path], prim)
		case Delete:
			byFile[x.Span.Path] = append(byFile[x.Span.Path], prim)
		case Replace:
			byFile[x.Span.Path] = append(byFile[x.Span.Path], prim)
		case Move:
			return nil, errors.New("edit: Move primitives are not yet supported")
		default:
			return nil, fmt.Errorf("edit: unknown primitive type %T", prim)
		}
	}

	out := make(map[string][]byte, len(files))
	for path, src := range files {
		dup := make([]byte, len(src))
		copy(dup, src)
		out[path] = dup
	}

	for path, prims := range byFile {
		src := out[path]
		newSrc, err := applyToFile(src, prims)
		if err != nil {
			return nil, err
		}
		out[path] = newSrc
	}
	return out, nil
}

// applyToFile applies a batch of Insert/Delete/Replace primitives targeting
// a single file.
func applyToFile(src []byte, prims []Primitive) ([]byte, error) {
	resolved := make([]Primitive, 0, len(prims))
	for _, prim := range prims {
		if ins, ok := prim.(Insert); ok && ins.Anchor.Offset == -1 {
			ins.Anchor.Offset = len(src)
			prim = ins
		}
		resolved = append(resolved, prim)
	}

	if err := checkBounds(resolved, len(src)); err != nil {
		return nil, err
	}
	if err := checkConflicts(resolved); err != nil {
		return nil, err
	}

	resolved = dedupePrimitives(resolved)

	sort.SliceStable(resolved, func(i, j int) bool {
		ki, kj := primSortKey(resolved[i]), primSortKey(resolved[j])
		if ki.offset != kj.offset {
			return ki.offset < kj.offset
		}
		if ki.kind != kj.kind {
			return ki.kind < kj.kind
		}
		return ki.side < kj.side
	})

	var out []byte
	pos := 0
	for _, prim := range resolved {
		switch x := prim.(type) {
		case Insert:
			if x.Anchor.Offset < pos {
				continue
			}
			out = append(out, src[pos:x.Anchor.Offset]...)
			pos = x.Anchor.Offset
			out = append(out, x.Text...)
		case Delete:
			if x.Span.Start < pos {
				continue
			}
			out = append(out, src[pos:x.Span.Start]...)
			pos = x.Span.End
		case Replace:
			if x.Span.Start < pos {
				continue
			}
			out = append(out, src[pos:x.Span.Start]...)
			out = append(out, x.Text...)
			pos = x.Span.End
		}
	}
	out = append(out, src[pos:]...)
	return out, nil
}

// sortKey is the comparison key used to order primitives within a file
// during application. Inserts at a boundary come before the
// Delete/Replace at the same boundary (kind Insert < kind Span), and
// Side=Before comes before Side=After at the same boundary.
type sortKey struct {
	offset int
	kind   int
	side   Side
}

func primSortKey(prim Primitive) sortKey {
	switch x := prim.(type) {
	case Insert:
		return sortKey{offset: x.Anchor.Offset, kind: 0, side: x.Side}
	case Delete:
		return sortKey{offset: x.Span.Start, kind: 1}
	case Replace:
		return sortKey{offset: x.Span.Start, kind: 1}
	}
	return sortKey{}
}

func checkBounds(prims []Primitive, fileLen int) error {
	for _, prim := range prims {
		switch x := prim.(type) {
		case Insert:
			if x.Anchor.Offset < 0 || x.Anchor.Offset > fileLen {
				return fmt.Errorf("edit: Insert offset %d out of range [0,%d] in %q (origin %q)",
					x.Anchor.Offset, fileLen, x.Anchor.Path, x.Origin())
			}
		case Delete:
			if err := checkSpanBounds("Delete", x.Span, fileLen, x.Origin()); err != nil {
				return err
			}
		case Replace:
			if err := checkSpanBounds("Replace", x.Span, fileLen, x.Origin()); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkSpanBounds(kind string, sp Span, fileLen int, origin string) error {
	if sp.Start < 0 || sp.End < sp.Start || sp.End > fileLen {
		return fmt.Errorf("edit: %s span [%d,%d) out of range [0,%d] in %q (origin %q)",
			kind, sp.Start, sp.End, fileLen, sp.Path, origin)
	}
	return nil
}

// checkConflicts reports any pair of primitives whose coordinates
// overlap in a way not permitted by the composition rules. O(n²) in
// the number of primitives per file — acceptable for today's plan
// sizes; revisit if that changes.
func checkConflicts(prims []Primitive) error {
	for i := range len(prims) {
		for j := i + 1; j < len(prims); j++ {
			if err := checkPair(prims[i], prims[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPair returns a *ConflictError if a and b can't coexist. Identical
// duplicates (same coordinates and payload) are not conflicts — they are
// silently deduplicated during application.
func checkPair(a, b Primitive) error {
	sa, sb := spanOf(a), spanOf(b)

	// Insert vs Insert.
	if sa == nil && sb == nil {
		ia := a.(Insert)
		ib := b.(Insert)
		if ia.Anchor.Offset != ib.Anchor.Offset || ia.Side != ib.Side {
			return nil
		}
		if ia.Text == ib.Text {
			return nil
		}
		return &ConflictError{A: a, B: b, Reason: "two Inserts at the same anchor and Side but different Text"}
	}

	// Insert vs span-carrying primitive.
	if sa == nil || sb == nil {
		var ins Insert
		var sp Span
		if sa == nil {
			ins = a.(Insert)
			sp = *sb
		} else {
			ins = b.(Insert)
			sp = *sa
		}
		if ins.Anchor.Path != sp.Path {
			return nil
		}
		if ins.Anchor.Offset > sp.Start && ins.Anchor.Offset < sp.End {
			return &ConflictError{A: a, B: b, Reason: "Insert falls inside a Delete/Replace range"}
		}
		return nil
	}

	// Span vs span.
	if sa.Path != sb.Path {
		return nil
	}
	if sa.End <= sb.Start || sb.End <= sa.Start {
		return nil
	}
	if *sa == *sb {
		ra, aIsReplace := a.(Replace)
		rb, bIsReplace := b.(Replace)
		switch {
		case aIsReplace && bIsReplace:
			if ra.Text == rb.Text {
				return nil
			}
			return &ConflictError{A: a, B: b, Reason: "two Replaces on the same span with different Text"}
		case !aIsReplace && !bIsReplace:
			return nil
		default:
			return &ConflictError{A: a, B: b, Reason: "Delete and Replace on the same span"}
		}
	}
	return &ConflictError{A: a, B: b, Reason: "overlapping spans"}
}

// spanOf returns a pointer to the Span of a Delete or Replace, or nil
// when the primitive is an Insert or Move.
func spanOf(prim Primitive) *Span {
	switch x := prim.(type) {
	case Delete:
		sp := x.Span
		return &sp
	case Replace:
		sp := x.Span
		return &sp
	}
	return nil
}

// dedupePrimitives drops repeated primitives with identical coordinates
// and payload. Preserves the first occurrence in input order.
func dedupePrimitives(prims []Primitive) []Primitive {
	seen := make(map[string]bool, len(prims))
	out := make([]Primitive, 0, len(prims))
	for _, prim := range prims {
		key := primDedupeKey(prim)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, prim)
	}
	return out
}

func primDedupeKey(prim Primitive) string {
	switch x := prim.(type) {
	case Insert:
		return fmt.Sprintf("I\x00%s\x00%d\x00%d\x00%s",
			x.Anchor.Path, x.Anchor.Offset, x.Side, x.Text)
	case Delete:
		return fmt.Sprintf("D\x00%s\x00%d\x00%d",
			x.Span.Path, x.Span.Start, x.Span.End)
	case Replace:
		return fmt.Sprintf("R\x00%s\x00%d\x00%d\x00%s",
			x.Span.Path, x.Span.Start, x.Span.End, x.Text)
	}
	return ""
}
