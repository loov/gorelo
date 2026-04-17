package relo

import (
	"go/ast"

	ed "github.com/loov/gorelo/edit"
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

	// Post-resolution validators (see validate.go).
	if err := checkUnexportedCrossPkg(resolved, fmInfos); err != nil {
		return nil, err
	}

	// Phase 2-3: compute spans with block semantics.
	spans, err := computeSpans(ix, resolved, plan)
	if err != nil {
		return nil, err
	}

	// Phase 4-5: check build constraints and detect conflicts.
	checkConstraints(resolved, plan)
	resolvedGroups := buildResolvedGroups(resolved)
	if err := detectConflicts(ix, resolved, spans, resolvedGroups, opts, plan); err != nil {
		return nil, err
	}

	// Phase 6: compute rename edits into the shared edit.Plan.
	edits := &ed.Plan{}
	movedSpans := buildMovedSpanIndex(resolved, spans)
	detachGroups := buildDetachGroups(resolved)
	computeRenames(ix, resolved, spans, movedSpans, detachGroups, opts, plan, edits)

	// Phase 7: import changes accumulate into importChanges.
	// rewriteSpanQualifiers (called from emitCrossFileExtraction and
	// assembleFileMoves) registers all source-side imports the moved
	// span actually uses; computeDetachEdits and computeConsumerEdits
	// register the imports specific to their rewrites.
	importChanges := &importSet{byFile: make(map[string]*importChange)}
	warnNontransferableImports(ix, resolved, plan)

	// Phase 7a: compute detach/attach edits.
	computeDetachEdits(ix, resolved, spans, edits, importChanges, plan)

	// Phase 7b: compute consumer edits (rewrite files that import moved symbols).
	computeConsumerEdits(ix, resolved, spans, movedSpans, detachGroups, edits, importChanges, opts, plan)

	// Phase 7c: emit cross-file extraction (Move primitives + carried
	// qualification edits) so plan.Apply produces both source-side
	// deletions and target-side appended content.
	emitCrossFileExtraction(ix, resolved, resolvedGroups, spans, edits, importChanges)

	// Phase 7d: emit whole-file moves (Move + qualification edits +
	// package-clause Replace) onto the shared Plan.
	emitFileMoveEdits(ix, fmInfos, resolved, resolvedGroups, spans, edits, importChanges)

	// Phase 8: assemble file edits.
	assemble(ix, resolved, spans, edits, importChanges, fmInfos, opts, plan)

	return plan, nil
}

// Apply writes a Plan to disk.
func Apply(plan *Plan) error {
	return applyPlan(plan)
}
