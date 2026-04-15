package relo

import (
	"go/ast"

	"github.com/loov/gorelo/mast"
)

// Relo describes a single relocation or rename instruction.
type Relo struct {
	Ident    *ast.Ident // must be tracked by Index.Group() with Kind == Def
	MoveTo   string     // target file path; empty = same file (rename only)
	Rename   string     // new name; empty = keep original
	Detach   bool       // convert method to standalone function
	MethodOf string     // convert function to method on this type
}

// FileMove describes a whole-file relocation. Every top-level declaration in
// From is transferred to To, preserving source ordering and file-level
// comments. Cross-package moves additionally rewrite references in consumer
// files via the standard relo machinery.
type FileMove struct {
	From string // source file path (as known to the mast.Index)
	To   string // destination file path
}

// Plan is the result of Compile: a set of file edits that implement the relos.
type Plan struct {
	Edits    []FileEdit
	Warnings Warnings
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
	Stubs bool // generate //go:fix inline alias stubs
}

func (o *Options) stubsEnabled() bool { return o != nil && o.Stubs }

// Compile builds a Plan from a set of Relo and FileMove instructions against
// a mast.Index.
func Compile(ix *mast.Index, relos []Relo, fileMoves []FileMove, opts *Options) (*Plan, error) {
	if opts == nil {
		opts = &Options{}
	}
	plan := &Plan{}

	// Expand file moves into per-decl relos and collect the moves for the
	// whole-file assembly pass. userRelos gets mutated so that explicit
	// renames or attaches targeting idents inside a moved file inherit the
	// file-move destination.
	userRelos := make([]Relo, len(relos))
	copy(userRelos, relos)
	expanded, fmInfos, err := expandFileMoves(ix, fileMoves, userRelos)
	if err != nil {
		return nil, err
	}
	relos = append(userRelos, expanded...)

	// Phase 0-1: validate, deduplicate, synthesize.
	resolved, err := resolve(ix, relos, fmInfos, plan)
	if err != nil {
		return nil, err
	}
	tagFileMoves(resolved, fmInfos)
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
	if err := detectConflicts(ix, resolved, spans, opts, plan); err != nil {
		return nil, err
	}

	// Phase 6: compute rename edits.
	renameEdits := computeRenames(ix, resolved, spans, opts, plan)

	// Phase 7: compute import changes.
	importChanges := computeImports(ix, resolved, spans, plan)

	// Phase 7a: compute detach/attach edits (after imports for cross-pkg qualification).
	computeDetachEdits(ix, resolved, spans, renameEdits, importChanges, plan)

	// Phase 7b: compute consumer edits (rewrite files that import moved symbols).
	computeConsumerEdits(ix, resolved, spans, renameEdits, importChanges, opts, plan)

	// Phase 8: assemble file edits.
	assemble(ix, resolved, spans, renameEdits, importChanges, fmInfos, opts, plan)

	return plan, nil
}

// Apply writes a Plan to disk.
func Apply(plan *Plan) error {
	return applyPlan(plan)
}
