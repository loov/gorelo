package relo

import (
	"path/filepath"

	"github.com/loov/gorelo/mast"
)

// finalloc.go is the single source of truth for "post-operation identity"
// of a resolvedRelo or a mast.Group caught up in a Compile run. Every
// rewrite-phase caller (detach, assemble, consumer, rename edits, imports)
// must route through these helpers when it needs to know "what name will
// this declaration have" or "which directory/package will it live in"
// after all the relos in this run are applied.
//
// Reading rr.Group.Name, rr.File.Pkg.Path, rr.DefIdent.Ident.Name, or
// pkg.Path directly in a rewrite context loses concurrent rename / move
// information and has caused real bugs (bc45f06, 7d8c120). The TestNoRaw*
// lint in internal_test.go enforces that detach.go and assemble.go do not
// reintroduce the bug-prone patterns.

// finalName returns the identifier name this relo will have after the run.
func finalName(rr *resolvedRelo) string {
	return rr.TargetName
}

// finalDir returns the directory the relo's declaration will live in
// after the run.
func finalDir(rr *resolvedRelo) string {
	return filepath.Dir(rr.TargetFile)
}

// finalImportPath returns the import path of the package the relo's
// declaration will live in after the run, or "" if it cannot be resolved.
func finalImportPath(rr *resolvedRelo) string {
	return guessImportPath(finalDir(rr))
}

// finalPkgName returns the short package name (as used in a pkg.Name
// qualifier) of the package the relo's declaration will live in after
// the run. When the destination package exists in ix, its declared name
// wins; otherwise the directory basename is used.
//
//lint:ignore U1000 preventive API; emission sites should call this instead of recomputing
func finalPkgName(rr *resolvedRelo, ix *mast.Index) string {
	return packageLocalName(ix, finalDir(rr))
}

// finalNameForGroup returns the post-operation name for grp, looking
// through the resolved set. If grp is not being renamed in this run,
// returns grp.Name.
//
//lint:ignore U1000 preventive API; emission sites should call this instead of reading grp.Name
func finalNameForGroup(resolved []*resolvedRelo, grp *mast.Group) string {
	if rr := resolvedForGroup(resolved, grp); rr != nil {
		return rr.TargetName
	}
	return grp.Name
}

// finalDirForGroup returns the post-operation directory for grp,
// looking through the resolved set. If grp is not being moved in this
// run, returns the directory of its existing def file (or "" if none).
//
//lint:ignore U1000 preventive API; referenced by finalloc_lint_test.go hints
func finalDirForGroup(resolved []*resolvedRelo, grp *mast.Group) string {
	if rr := resolvedForGroup(resolved, grp); rr != nil {
		return finalDir(rr)
	}
	for _, id := range grp.Idents {
		if id.Kind == mast.Def && id.File != nil {
			return filepath.Dir(id.File.Path)
		}
	}
	return ""
}

// finalImportPathForGroup returns the post-operation import path for
// grp's package, looking through the resolved set. Returns "" if the
// directory cannot be resolved to an import path.
//
//lint:ignore U1000 preventive API; referenced by finalloc_lint_test.go hints
func finalImportPathForGroup(resolved []*resolvedRelo, grp *mast.Group) string {
	dir := finalDirForGroup(resolved, grp)
	if dir == "" {
		return ""
	}
	return guessImportPath(dir)
}
