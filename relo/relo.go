package relo

import (
	"go/ast"

	"github.com/loov/gorelo/mast"
)

// Relo describes a single relocation or rename instruction.
type Relo struct {
	Ident  *ast.Ident // must be tracked by Index.Group() with Kind == Def
	MoveTo string     // target file path; empty = same file (rename only)
	Rename string     // new name; empty = keep original
}

// Plan is the result of Compile: a set of file edits that implement the relos.
type Plan struct {
	Edits    []FileEdit
	Warnings []string
}

// FileEdit describes a change to a single file.
type FileEdit struct {
	Path     string
	IsNew    bool
	IsDelete bool
	Content  string
}

// Options controls optional behaviors of Compile.
type Options struct {
	Stubs            bool   // generate //go:fix inline alias stubs
	RewriteConsumers bool   // walk module tree to rewrite importers
	ModuleRoot       string // auto-detected if empty
}

// Compile builds a Plan from a set of Relo instructions against a mast.Index.
func Compile(ix *mast.Index, relos []Relo, opts *Options) (*Plan, error) {
	if opts == nil {
		opts = &Options{}
	}
	plan := &Plan{}

	// Phase 0-1: validate, deduplicate, synthesize.
	resolved, err := resolve(ix, relos, plan)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return plan, nil
	}

	// Phase 2-3: compute spans with block semantics.
	spans, err := computeSpans(ix, resolved, plan)
	if err != nil {
		return nil, err
	}

	// Phase 4-5: check build constraints and detect conflicts.
	checkConstraints(resolved, plan)
	if err := detectConflicts(ix, resolved, plan); err != nil {
		return nil, err
	}

	// Phase 6: compute rename edits.
	renameEdits := computeRenames(ix, resolved, spans)

	// Phase 7: compute import changes.
	importChanges := computeImports(ix, resolved, spans, plan)

	// Phase 8: assemble file edits.
	assemble(ix, resolved, spans, renameEdits, importChanges, opts, plan)

	return plan, nil
}

// Apply writes a Plan to disk.
func Apply(plan *Plan) error {
	return applyPlan(plan)
}
