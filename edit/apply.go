package edit

import (
	"fmt"
	"sort"
	"strings"
)

// ConflictError is returned by Plan.Apply when two primitives overlap in
// a way that is not resolved by the carrying or endpoint-ordering rules.
//
// When a debug Plan is used, the conflicting primitives' Frames() method
// returns the call stacks recorded at the point they were added, which
// is the typical way to locate the emission sites responsible for a
// conflict.
type ConflictError struct {
	A, B   Primitive
	Reason string
}

func (e *ConflictError) Error() string {
	msg := fmt.Sprintf("edit conflict between %q and %q: %s", e.A.Origin(), e.B.Origin(), e.Reason)
	if loc := topFrame(e.A); loc != "" {
		msg += fmt.Sprintf("\n  A added at %s", loc)
	}
	if loc := topFrame(e.B); loc != "" {
		msg += fmt.Sprintf("\n  B added at %s", loc)
	}
	return msg
}

// topFrame returns a compact "file:line function" string for the outermost
// recorded frame of prim, or "" when no frames were captured.
func topFrame(prim Primitive) string {
	frames := prim.Frames()
	if len(frames) == 0 {
		return ""
	}
	f := frames[0]
	return fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Function)
}

// Apply applies the plan to files and returns the resulting contents.
//
// files is a map from file path to original byte contents. Paths not
// present in files but referenced by a primitive are treated as if
// their original content were empty (Move destinations may create new
// files this way).
//
// On any composition conflict Apply returns a *ConflictError; on a
// bounds error or other structural problem it returns a plain error.
func (p *Plan) Apply(files map[string][]byte) (map[string][]byte, error) {
	for _, prim := range p.prims {
		if err := validatePrimitive(prim, files); err != nil {
			return nil, err
		}
	}

	parent, err := classifyContainment(p.prims)
	if err != nil {
		return nil, err
	}

	children := make(map[int][]int, len(p.prims))
	for i := range p.prims {
		children[parent[i]] = append(children[parent[i]], i)
	}

	realized, err := computeRealizedContent(p.prims, children, files)
	if err != nil {
		return nil, err
	}

	topLevel, groupInserts, err := lowerMoves(p.prims, parent, realized)
	if err != nil {
		return nil, err
	}
	topLevel = append(topLevel, groupInserts...)

	byFile := make(map[string][]Primitive)
	for _, prim := range topLevel {
		byFile[pathOf(prim)] = append(byFile[pathOf(prim)], prim)
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

// pathOf returns the file path a primitive addresses.
func pathOf(prim Primitive) string {
	switch x := prim.(type) {
	case Insert:
		return x.Anchor.Path
	case Delete:
		return x.Span.Path
	case Replace:
		return x.Span.Path
	}
	return ""
}

// validatePrimitive checks a primitive's coordinates against the input
// file lengths. Move destinations on new files are permitted; all other
// coordinates must be within their file's bounds.
func validatePrimitive(prim Primitive, files map[string][]byte) error {
	switch x := prim.(type) {
	case Insert:
		src := files[x.Anchor.Path]
		off := x.Anchor.Offset
		if off == -1 {
			return nil
		}
		if off < 0 || off > len(src) {
			return fmt.Errorf("edit: Insert offset %d out of range [0,%d] in %q (origin %q)",
				off, len(src), x.Anchor.Path, x.Origin())
		}
	case Delete:
		return checkSpanBounds("Delete", x.Span, len(files[x.Span.Path]), x.Origin())
	case Replace:
		return checkSpanBounds("Replace", x.Span, len(files[x.Span.Path]), x.Origin())
	case Move:
		if err := checkSpanBounds("Move source", x.Span, len(files[x.Span.Path]), x.Origin()); err != nil {
			return err
		}
		dstLen := len(files[x.Dest.Path])
		off := x.Dest.Offset
		if off == -1 {
			return nil
		}
		if off < 0 || off > dstLen {
			return fmt.Errorf("edit: Move destination offset %d out of range [0,%d] in %q (origin %q)",
				off, dstLen, x.Dest.Path, x.Origin())
		}
	}
	return nil
}

// classifyContainment returns, for each primitive, the index of the
// innermost Move that contains it, or -1 if none does. Two Moves that
// overlap without nesting are reported as ConflictError.
func classifyContainment(prims []Primitive) ([]int, error) {
	parent := make([]int, len(prims))
	for i := range parent {
		parent[i] = -1
	}

	var moveIdx []int
	for i, prim := range prims {
		if _, ok := prim.(Move); ok {
			moveIdx = append(moveIdx, i)
		}
	}

	// Move-vs-Move: detect overlap-without-nesting as conflict; record
	// strict containment into parent.
	for _, i := range moveIdx {
		for _, j := range moveIdx {
			if i == j {
				continue
			}
			mi := prims[i].(Move)
			mj := prims[j].(Move)
			switch spanRelation(mi.Span, mj.Span) {
			case relEqual:
				return nil, &ConflictError{A: prims[i], B: prims[j], Reason: "two Moves share the exact same source span"}
			case relOverlapping:
				return nil, &ConflictError{A: prims[i], B: prims[j], Reason: "Move spans overlap without nesting"}
			case relContained:
				// mi is inside mj; mj is a candidate parent.
				updateInnermost(parent, prims, i, j)
			}
		}
	}

	// Non-Move vs Move: classify containment; detect overlap as conflict.
	for i, prim := range prims {
		if _, isMove := prim.(Move); isMove {
			continue
		}
		for _, j := range moveIdx {
			mv := prims[j].(Move)
			rel, err := relateToMove(prim, mv)
			if err != nil {
				return nil, &ConflictError{A: prim, B: prims[j], Reason: err.Error()}
			}
			if rel == relContained {
				updateInnermost(parent, prims, i, j)
			}
		}
	}

	return parent, nil
}

// updateInnermost records j as the containing Move of i if j is strictly
// smaller than any previously recorded containing Move.
func updateInnermost(parent []int, prims []Primitive, i, j int) {
	if parent[i] == -1 {
		parent[i] = j
		return
	}
	prev := prims[parent[i]].(Move).Span
	next := prims[j].(Move).Span
	if spanArea(next) < spanArea(prev) {
		parent[i] = j
	}
}

func spanArea(s Span) int { return s.End - s.Start }

type spanRel int

const (
	relDisjoint spanRel = iota
	relEqual
	relContained // a inside b (strict)
	relContains  // b inside a (strict)
	relOverlapping
)

// spanRelation classifies the geometric relationship between two spans.
// Spans in different files are treated as disjoint.
func spanRelation(a, b Span) spanRel {
	if a.Path != b.Path {
		return relDisjoint
	}
	if a == b {
		return relEqual
	}
	if a.End <= b.Start || b.End <= a.Start {
		return relDisjoint
	}
	if a.Start >= b.Start && a.End <= b.End {
		return relContained
	}
	if b.Start >= a.Start && b.End <= a.End {
		return relContains
	}
	return relOverlapping
}

// relateToMove classifies a non-Move primitive's relationship to a Move.
// Returns relContained if the primitive is carried; relDisjoint otherwise.
// Partial overlap returns an error message describing the conflict.
func relateToMove(prim Primitive, mv Move) (spanRel, error) {
	switch x := prim.(type) {
	case Insert:
		if x.Anchor.Path != mv.Span.Path {
			return relDisjoint, nil
		}
		off := x.Anchor.Offset
		if off <= mv.Span.Start || off >= mv.Span.End {
			return relDisjoint, nil
		}
		return relContained, nil
	case Delete:
		return classifySpanIntoMove(x.Span, mv)
	case Replace:
		return classifySpanIntoMove(x.Span, mv)
	}
	return relDisjoint, nil
}

func classifySpanIntoMove(sp Span, mv Move) (spanRel, error) {
	switch spanRelation(sp, mv.Span) {
	case relDisjoint:
		return relDisjoint, nil
	case relContained, relEqual:
		return relContained, nil
	case relContains:
		return 0, fmt.Errorf("Delete/Replace span fully contains a Move span")
	case relOverlapping:
		return 0, fmt.Errorf("Delete/Replace span partially overlaps a Move span")
	}
	return relDisjoint, nil
}

// computeRealizedContent produces the emitted bytes for each Move,
// indexed by the Move's position in prims. Carried non-Move primitives
// are applied in span-relative coordinates; carried Moves contribute a
// Delete for their source range (their bytes relocate separately).
func computeRealizedContent(prims []Primitive, children map[int][]int, files map[string][]byte) (map[int][]byte, error) {
	realized := make(map[int][]byte)
	for i, prim := range prims {
		mv, ok := prim.(Move)
		if !ok {
			continue
		}
		src := files[mv.Span.Path]
		span := src[mv.Span.Start:mv.Span.End]

		var rel []Primitive
		for _, ci := range children[i] {
			switch x := prims[ci].(type) {
			case Insert:
				x.Anchor = Anchor{Path: "<carry>", Offset: x.Anchor.Offset - mv.Span.Start}
				rel = append(rel, x)
			case Delete:
				x.Span = Span{Path: "<carry>", Start: x.Span.Start - mv.Span.Start, End: x.Span.End - mv.Span.Start}
				rel = append(rel, x)
			case Replace:
				x.Span = Span{Path: "<carry>", Start: x.Span.Start - mv.Span.Start, End: x.Span.End - mv.Span.Start}
				rel = append(rel, x)
			case Move:
				rel = append(rel, Delete{
					Span:   Span{Path: "<carry>", Start: x.Span.Start - mv.Span.Start, End: x.Span.End - mv.Span.Start},
					origin: x.origin,
				})
			}
		}

		out, err := applyToFile(span, rel)
		if err != nil {
			return nil, err
		}
		realized[i] = applyMoveOptions(out, mv.Options)
	}
	return realized, nil
}

// lowerMoves converts Moves into (top-level Delete at source, top-level
// grouped Insert at destination) pairs and returns the remaining
// top-level non-Move primitives alongside the synthesized Inserts.
//
// Nested Moves do not emit a source-side Delete (the enclosing Move
// already covers their source range) but do emit a destination-side
// Insert.
func lowerMoves(prims []Primitive, parent []int, realized map[int][]byte) ([]Primitive, []Primitive, error) {
	var topLevel []Primitive
	var pending []pendingInsert

	for i, prim := range prims {
		if mv, ok := prim.(Move); ok {
			pending = append(pending, pendingInsert{
				dest:       mv.Dest,
				keyword:    mv.Options.GroupKeyword,
				content:    realized[i],
				origin:     mv.origin,
				sourceSpan: mv.Span,
				render:     mv.Options.GroupRender,
				order:      mv.Options.Order,
			})
			if parent[i] == -1 {
				topLevel = append(topLevel, Delete{Span: mv.Span, origin: mv.origin})
			}
			continue
		}
		if parent[i] == -1 {
			topLevel = append(topLevel, prim)
		}
	}

	groupInserts, err := mergeMoveInserts(pending)
	if err != nil {
		return nil, nil, err
	}
	return topLevel, groupInserts, nil
}

// mergeMoveInserts consolidates pending Move-destination Inserts into one
// Insert per anchor. Items sharing an anchor are sorted by source span;
// consecutive items with the same non-empty GroupKeyword are wrapped in
// one `keyword (…)` block, while items with empty GroupKeyword (or that
// transition between keywords) emit their content directly. This lets
// callers append multiple Moves to the same destination — including
// interleaved const/var/type sections — without an artificial conflict.
func mergeMoveInserts(pending []pendingInsert) ([]Primitive, error) {
	type anchorKey struct {
		path   string
		offset int
	}
	byAnchor := make(map[anchorKey][]pendingInsert)
	for _, mi := range pending {
		ak := anchorKey{mi.dest.Path, mi.dest.Offset}
		byAnchor[ak] = append(byAnchor[ak], mi)
	}

	// Stable order for anchor iteration.
	aks := make([]anchorKey, 0, len(byAnchor))
	for ak := range byAnchor {
		aks = append(aks, ak)
	}
	sort.SliceStable(aks, func(i, j int) bool {
		if aks[i].path != aks[j].path {
			return aks[i].path < aks[j].path
		}
		return aks[i].offset < aks[j].offset
	})

	var out []Primitive
	for _, ak := range aks {
		items := byAnchor[ak]
		// Within an anchor, sort by explicit Order first, then by source
		// span for deterministic output independent of Plan insertion order.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].order != items[j].order {
				return items[i].order < items[j].order
			}
			return sourceLess(items[i].sourceSpan, items[j].sourceSpan)
		})

		var content []byte
		i := 0
		for i < len(items) {
			kw := items[i].keyword
			render := items[i].render
			j := i + 1
			for j < len(items) && items[j].keyword == kw {
				if render == nil {
					render = items[j].render
				}
				j++
			}
			switch {
			case render != nil:
				bodies := make([][]byte, 0, j-i)
				for k := i; k < j; k++ {
					bodies = append(bodies, items[k].content)
				}
				content = append(content, render(bodies)...)
			case kw == "":
				for k := i; k < j; k++ {
					content = append(content, items[k].content...)
				}
			default:
				content = append(content, kw...)
				content = append(content, " (\n"...)
				for k := i; k < j; k++ {
					content = append(content, items[k].content...)
				}
				content = append(content, ")\n"...)
			}
			i = j
		}

		out = append(out, Insert{
			Anchor: Anchor{Path: ak.path, Offset: ak.offset},
			Text:   string(content),
			Side:   Before,
			origin: items[0].origin,
		})
	}
	return out, nil
}

// sourceLess orders spans by (Path, Start, End) for stable deterministic
// merging.
func sourceLess(a, b Span) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.Start != b.Start {
		return a.Start < b.Start
	}
	return a.End < b.End
}

// applyMoveOptions applies TrimLeadingBlank, Dedent, and AppendNewline
// to the realized content of a Move, in that order.
func applyMoveOptions(b []byte, opts MoveOptions) []byte {
	s := string(b)
	if opts.TrimLeadingBlank {
		for strings.HasPrefix(s, "\n") {
			s = s[1:]
		}
	}
	if opts.Dedent {
		s = dedent(s)
	}
	if opts.AppendNewline && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s)
}

// dedent strips the common leading run of spaces-and-tabs from each
// non-blank line of s. Blank lines are left as-is.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := 0
		for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
			n++
		}
		if minIndent == -1 || n < minIndent {
			minIndent = n
		}
	}
	if minIndent <= 0 {
		return s
	}
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}

// pendingInsert represents a Move destination Insert awaiting group
// merging. sourceSpan is the Move's original source range; it is used
// to order merged content deterministically regardless of the order in
// which primitives were added to the Plan. render, when non-nil for at
// least one item in a same-keyword run, formats the group; otherwise
// the built-in fallback wraps in `keyword (\n…)\n` (or concatenates
// when keyword is empty).
type pendingInsert struct {
	dest       Anchor
	keyword    string
	content    []byte
	origin     string
	sourceSpan Span
	render     GroupRenderer
	order      int
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
	// Fully-contained overlap (one span strictly inside the other) is
	// not flagged as a conflict: applyToFile's left-to-right walk drops
	// the contained one when its Start has already been passed.
	if (sa.Start >= sb.Start && sa.End <= sb.End) ||
		(sb.Start >= sa.Start && sb.End <= sa.End) {
		return nil
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
