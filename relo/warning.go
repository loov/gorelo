package relo

import (
	"fmt"

	"github.com/loov/gorelo/mast"
)

// Warning is an advisory message emitted during compilation.
type Warning struct {
	// File is the source file path related to the warning, if known.
	File string
	// Line is the 1-based line number in File, or 0 if unknown.
	Line int
	// Message is the warning text.
	Message string
}

// String formats the warning, prepending "file:line: " when available.
func (w Warning) String() string {
	if w.File != "" {
		if w.Line > 0 {
			return fmt.Sprintf("%s:%d: %s", w.File, w.Line, w.Message)
		}
		return w.File + ": " + w.Message
	}
	return w.Message
}

// Warnf creates a Warning without source location.
func Warnf(format string, args ...any) Warning {
	return Warning{Message: fmt.Sprintf(format, args...)}
}

// Warnings collects advisory messages emitted during compilation.
type Warnings []Warning

// Add appends one or more warnings.
func (w *Warnings) Add(warnings ...Warning) {
	*w = append(*w, warnings...)
}

// Addf appends a formatted warning without source location.
func (w *Warnings) Addf(format string, args ...any) {
	*w = append(*w, Warnf(format, args...))
}

// AddAtf appends a warning with file and line from a resolvedRelo.
func (w *Warnings) AddAtf(rr *resolvedRelo, ix *mast.Index, format string, args ...any) {
	warn := Warning{Message: fmt.Sprintf(format, args...)}
	if rr.File != nil {
		warn.File = rr.File.Path
	}
	if rr.DefIdent != nil && ix != nil {
		pos := ix.Fset.Position(rr.DefIdent.Ident.Pos())
		if pos.IsValid() {
			warn.File = pos.Filename
			warn.Line = pos.Line
		}
	}
	*w = append(*w, warn)
}

// Strings returns all warning messages as formatted strings.
func (w Warnings) Strings() []string {
	s := make([]string, len(w))
	for i, warn := range w {
		s[i] = warn.String()
	}
	return s
}
