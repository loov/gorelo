// Package edit composes byte-level text edits addressed in original-source
// coordinates. Callers build a Plan of primitives (Insert, Delete, Replace,
// Move) and invoke Plan.Apply to produce the resulting file contents.
//
// The package has no knowledge of Go syntax or AST: primitives carry byte
// offsets into whatever input files the caller supplies, and the applier
// performs position tracking so that primitives compose regardless of the
// order in which they are emitted.
package edit

import (
	"runtime"
)

// Anchor points at a byte offset in a named file.
//
// Offset values are interpreted against the original (pre-Apply) contents.
// Special value -1 for Offset means end-of-file.
type Anchor struct {
	Path   string
	Offset int
}

// Span is a half-open byte range [Start, End) in a named file, addressed
// against the original (pre-Apply) contents.
type Span struct {
	Path       string
	Start, End int
}

// Side disambiguates the ordering of two Inserts that share an Anchor.
// An Insert with Side=Before lands immediately before any Insert with
// Side=After at the same offset.
type Side int

const (
	Before Side = iota
	After
)

// MoveOptions configures how a Move's relocated bytes are emitted at the
// destination.
type MoveOptions struct {
	// TrimLeadingBlank strips leading blank lines from the moved bytes.
	TrimLeadingBlank bool

	// AppendNewline ensures the emitted bytes end with a newline.
	AppendNewline bool

	// Dedent strips the common leading whitespace from each line of the
	// moved bytes (useful when relocating an indented grouped spec).
	Dedent bool

	// GroupKeyword groups consecutive Moves into the same destination
	// anchor that share this value. The grouping itself (sort by source
	// span, segment by keyword) is positional and language-agnostic;
	// how each group renders is controlled by GroupRender (or the
	// built-in fallback when GroupRender is nil).
	GroupKeyword string

	// GroupRender, when set on at least one Move in a same-anchor +
	// same-GroupKeyword run, formats the group: it receives each item's
	// realized content in source-span order and returns the bytes the
	// group contributes at the destination. Use this to customize block
	// wrapping (e.g., Go's `keyword (\n…)\n` with tab-indented items
	// and an inline single-item form). When nil, the built-in fallback
	// emits `keyword (\n…)\n` with no per-line indentation.
	GroupRender GroupRenderer

	// Order controls the position of this Move's content relative to
	// other Moves at the same destination anchor. Lower values sort
	// first; ties are broken by source span. Default is 0.
	Order int
}

// GroupRenderer formats a same-anchor + same-GroupKeyword run of Move
// items at the destination. Items are passed in source-span order;
// the returned bytes are inserted at the destination as one chunk.
type GroupRenderer func(items [][]byte) []byte

// Primitive is an edit operation in original-source coordinates.
//
// Concrete primitives are Insert, Delete, Replace, and Move.
type Primitive interface {
	// Origin returns the free-form diagnostic tag the emission site
	// supplied when adding the primitive to a Plan.
	Origin() string

	// Frames returns the call frames captured when the primitive was
	// added to a debug Plan, outermost call first.
	// Returns nil for primitives added to a non-debug Plan.
	Frames() []runtime.Frame

	primitive()
}

// Insert places Text at Anchor. Side disambiguates the ordering of two
// Inserts at the same anchor.
type Insert struct {
	Anchor Anchor
	Text   string
	Side   Side

	origin  string
	callers callers
}

func (i Insert) Origin() string          { return i.origin }
func (i Insert) Frames() []runtime.Frame { return i.callers.resolve() }
func (Insert) primitive()                {}

// Delete removes the bytes covered by Span.
type Delete struct {
	Span Span

	origin  string
	callers callers
}

func (d Delete) Origin() string          { return d.origin }
func (d Delete) Frames() []runtime.Frame { return d.callers.resolve() }
func (Delete) primitive()                {}

// Replace substitutes Text for the bytes covered by Span. It is an atomic
// primitive (rather than a Delete + Insert pair) so that rename-like
// operations remain one primitive for conflict detection.
type Replace struct {
	Span Span
	Text string

	origin  string
	callers callers
}

func (r Replace) Origin() string          { return r.origin }
func (r Replace) Frames() []runtime.Frame { return r.callers.resolve() }
func (Replace) primitive()                {}

// Move relocates the bytes covered by Span to Dest. Any Insert, Delete,
// or Replace whose coordinates fall inside Span is automatically carried
// with the moved bytes.
type Move struct {
	Span    Span
	Dest    Anchor
	Options MoveOptions

	origin  string
	callers callers
}

func (m Move) Origin() string          { return m.origin }
func (m Move) Frames() []runtime.Frame { return m.callers.resolve() }
func (Move) primitive()                {}
