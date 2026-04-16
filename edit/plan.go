package edit

import "runtime"

// framesCaptured is the number of call frames recorded per primitive
// when a Plan has debug enabled.
const framesCaptured = 3

// callers is a compact record of the program counters captured when a
// primitive was added to a debug Plan. Storing raw PCs keeps each
// primitive small and defers the cost of symbol resolution to Frames().
type callers [framesCaptured]uintptr

// resolve unwinds the recorded program counters into runtime.Frames.
// Returns nil when no PCs were captured (e.g., for primitives added to
// a non-debug plan).
func (c callers) resolve() []runtime.Frame {
	n := 0
	for _, pc := range c {
		if pc == 0 {
			break
		}
		n++
	}
	if n == 0 {
		return nil
	}
	fr := runtime.CallersFrames(c[:n])
	out := make([]runtime.Frame, 0, n)
	for {
		f, more := fr.Next()
		out = append(out, f)
		if !more {
			break
		}
	}
	return out
}

// Plan is an append-only collection of primitives. The zero value is an
// empty non-debug Plan ready for use.
type Plan struct {
	// Debug plans are useful while developing emission code: when two
	// passes emit conflicting instructions, the resulting ConflictError
	// points directly at the two source locations responsible.
	Debug bool

	prims []Primitive
}

// Insert appends an Insert primitive to the plan.
func (p *Plan) Insert(anchor Anchor, text string, side Side, origin string) {
	p.prims = append(p.prims, Insert{
		Anchor: anchor, Text: text, Side: side,
		origin: origin, callers: p.captureCallers(),
	})
}

// Delete appends a Delete primitive to the plan.
func (p *Plan) Delete(span Span, origin string) {
	p.prims = append(p.prims, Delete{
		Span:   span,
		origin: origin, callers: p.captureCallers(),
	})
}

// Replace appends a Replace primitive to the plan.
func (p *Plan) Replace(span Span, text string, origin string) {
	p.prims = append(p.prims, Replace{
		Span: span, Text: text,
		origin: origin, callers: p.captureCallers(),
	})
}

// Move appends a Move primitive to the plan.
func (p *Plan) Move(span Span, dest Anchor, opts MoveOptions, origin string) {
	p.prims = append(p.prims, Move{
		Span: span, Dest: dest, Options: opts,
		origin: origin, callers: p.captureCallers(),
	})
}

// Primitives returns a copy of the primitives accumulated in the plan,
// in insertion order.
func (p *Plan) Primitives() []Primitive {
	out := make([]Primitive, len(p.prims))
	copy(out, p.prims)
	return out
}

// captureCallers records up to framesCaptured PCs starting from the
// Plan method's immediate caller. Returns a zero-valued callers when
// debug is off.
func (p *Plan) captureCallers() callers {
	var c callers
	if !p.Debug {
		return c
	}
	// skip runtime.Callers, captureCallers, and the Plan method.
	runtime.Callers(3, c[:])
	return c
}
