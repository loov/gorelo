package relo

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

// finalDir returns the directory the relo's declaration will live in
// after the run.
func finalDir(rr *resolvedRelo) string {
	return rr.TargetDir
}

// finalImportPath returns the import path of the package the relo's
// declaration will live in after the run, or "" if it cannot be resolved.
func finalImportPath(rr *resolvedRelo) string {
	return guessImportPath(rr.TargetDir)
}
