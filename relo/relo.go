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
	reloByGroup    map[*mast.Group]*resolvedRelo

	// Caches populated lazily or once and reused across phases.
	importPathCache map[string]string          // dir → import path
	pkgByDir        map[string]*mast.Package   // dir → non-test package
	byTarget        map[string][]*resolvedRelo // target file → relos
	bySource        map[string][]*resolvedRelo // source file → relos
}

// Compile builds a Plan from a set of Relo and FileMove instructions against
// a mast.Index.
func Compile(ix *mast.Index, relos []Relo, fileMoves []FileMove, opts *Options) (*Plan, error) {
	if opts == nil {
		opts = &Options{}
	}

	cc := &compileCtx{
		ix:              ix,
		opts:            opts,
		plan:            &Plan{},
		importPathCache: make(map[string]string),
		pkgByDir:        buildPkgByDir(ix),
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
	cc.fmInfos = fmInfos
	relos = append(userRelos, expanded...)

	// Phase 0-1: validate, deduplicate, synthesize.
	resolved, err := resolve(ix, relos, fmInfos, cc.plan)
	if err != nil {
		return nil, err
	}
	tagFileMoves(resolved, fmInfos)
	if len(resolved) == 0 {
		return cc.plan, nil
	}
	cc.resolved = resolved

	// Post-resolution validators (see validate.go).
	if err := checkUnexportedCrossPkg(cc.resolved, cc.fmInfos); err != nil {
		return nil, err
	}

	// Phase 2-3: compute spans with block semantics.
	spans, err := computeSpans(cc)
	if err != nil {
		return nil, err
	}
	cc.spans = spans

	// Phase 4-5: check build constraints and detect conflicts.
	checkConstraints(cc)
	cc.resolvedGroups = buildResolvedGroups(cc.resolved)
	cc.reloByGroup = buildReloByGroup(cc.resolved)
	if err := detectConflicts(cc); err != nil {
		return nil, err
	}

	// Phase 6: compute rename edits into the shared edit.Plan.
	cc.edits = &ed.Plan{}
	cc.movedSpans = buildMovedSpanIndex(cc.resolved, cc.spans)
	computeRenames(cc)

	// Phase 7: import changes accumulate into cc.imports.
	cc.imports = &importSet{byFile: make(map[string]*importChange)}
	warnNontransferableImports(cc)

	// Phase 7a: compute detach/attach structural edits.
	computeDetachEdits(cc)

	// Phase 7b: compute consumer qualifier edits (rewrite files that import moved symbols).
	computeConsumerEdits(cc)

	// Phase 7c: rewrite qualifiers inside all cross-file-moved spans
	// (both per-decl extractions and file moves share one loop).
	rewriteAllCrossFileQualifiers(cc)

	// Phase 7d: emit Move primitives — per-span for per-decl
	// extractions, per-file for whole-file moves.
	emitCrossFileExtraction(cc)
	emitFileMoveEdits(cc)

	// Phase 8: assemble file edits.
	assemble(cc)

	return cc.plan, nil
}

// qualifyEdit describes how to qualify a reference to a group that is
// moving to a different directory than the observer.
type qualifyEdit struct {
	// LocalRef is true when observer and group end up in the same package —
	// any existing qualifier should be stripped and no import is needed.
	LocalRef bool
	// Qualifier is the package name the observer must use (empty when local).
	Qualifier string
	// ImportPath is the import the observer's file needs (empty when local).
	ImportPath string
}

// classifyRef determines how a reference to a group whose destination is
// groupDir should be qualified from the perspective of code in observerDir.
// Both directories must be absolute paths.
func (cc *compileCtx) classifyRef(groupDir, observerDir string) qualifyEdit {
	if groupDir == observerDir {
		return qualifyEdit{LocalRef: true}
	}
	impPath := cc.cachedImportPath(groupDir)
	return qualifyEdit{
		Qualifier:  cc.cachedPackageLocalName(groupDir),
		ImportPath: impPath,
	}
}

// cachedImportPath returns guessImportPath(dir) with memoization.
func (cc *compileCtx) cachedImportPath(dir string) string {
	if v, ok := cc.importPathCache[dir]; ok {
		return v
	}
	v := guessImportPath(dir)
	cc.importPathCache[dir] = v
	return v
}

// cachedPkgForDir returns the non-test package in dir, or nil. Cached.
func (cc *compileCtx) cachedPkgForDir(dir string) *mast.Package {
	if pkg, ok := cc.pkgByDir[dir]; ok {
		return pkg
	}
	return nil
}

// cachedPackageLocalName returns the package local name for dir, using cached lookups.
func (cc *compileCtx) cachedPackageLocalName(dir string) string {
	if pkg := cc.cachedPkgForDir(dir); pkg != nil {
		return pkg.Name
	}
	return guessImportLocalName(cc.cachedImportPath(dir))
}

// initGroupByTargetSource populates byTarget and bySource once.
func (cc *compileCtx) initGroupByTargetSource() {
	if cc.byTarget != nil {
		return
	}
	cc.byTarget = groupByTarget(cc.resolved)
	cc.bySource = groupBySource(cc.resolved)
}

// Apply writes a Plan to disk.
func Apply(plan *Plan) error {
	return applyPlan(plan)
}
