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

// compileCtx holds the shared pipeline state threaded through every
// phase of a Compile run. Each field is populated progressively as
// phases execute.
type compileCtx struct {
	ix      *mast.Index
	opts    *Options
	plan    *Plan
	fmInfos []*fileMoveInfo

	resolved       []*resolvedRelo
	spans          map[*resolvedRelo]*span
	edits          *ed.Plan
	imports        *importSet
	resolvedGroups map[*mast.Group]bool
	movedSpans     movedSpanIndex
}

// Compile builds a Plan from a set of Relo and FileMove instructions against
// a mast.Index.
func Compile(ix *mast.Index, relos []Relo, fileMoves []FileMove, opts *Options) (*Plan, error) {
	if opts == nil {
		opts = &Options{}
	}

	ctx := &compileCtx{
		ix:   ix,
		opts: opts,
		plan: &Plan{},
	}

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
	ctx.fmInfos = fmInfos
	relos = append(userRelos, expanded...)

	// Phase 0-1: validate, deduplicate, synthesize.
	resolved, err := resolve(ix, relos, fmInfos, ctx.plan)
	if err != nil {
		return nil, err
	}
	tagFileMoves(resolved, fmInfos)
	if len(resolved) == 0 {
		return ctx.plan, nil
	}
	ctx.resolved = resolved

	// Post-resolution validators (see validate.go).
	if err := checkUnexportedCrossPkg(ctx.resolved, ctx.fmInfos); err != nil {
		return nil, err
	}

	// Phase 2-3: compute spans with block semantics.
	spans, err := computeSpans(ctx)
	if err != nil {
		return nil, err
	}
	ctx.spans = spans

	// Phase 4-5: check build constraints and detect conflicts.
	checkConstraints(ctx)
	ctx.resolvedGroups = buildResolvedGroups(ctx.resolved)
	if err := detectConflicts(ctx); err != nil {
		return nil, err
	}

	// Phase 6: compute rename edits into the shared edit.Plan.
	ctx.edits = &ed.Plan{}
	ctx.movedSpans = buildMovedSpanIndex(ctx.resolved, ctx.spans)
	computeRenames(ctx)

	// Phase 7: import changes accumulate into ctx.imports.
	ctx.imports = &importSet{byFile: make(map[string]*importChange)}
	warnNontransferableImports(ctx)

	// Phase 7a: compute detach/attach structural edits.
	computeDetachEdits(ctx)

	// Phase 7b: compute consumer qualifier edits (rewrite files that import moved symbols).
	computeConsumerEdits(ctx)

	// Phase 7c: emit cross-file extraction (Move primitives + carried
	// qualification edits) so plan.Apply produces both source-side
	// deletions and target-side appended content.
	emitCrossFileExtraction(ctx)

	// Phase 7d: emit whole-file moves (Move + carried qualification
	// edits + package-clause Replace) onto the shared Plan.
	emitFileMoveEdits(ctx)

	// Phase 8: assemble file edits.
	assemble(ctx)

	return ctx.plan, nil
}

// Apply writes a Plan to disk.
func Apply(plan *Plan) error {
	return applyPlan(plan)
}
