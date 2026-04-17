package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/loov/gorelo/mast"
)

// checkConstraints warns about build constraint issues (phase 4).
func checkConstraints(ctx *compileCtx) {
	plan := ctx.plan
	ctx.initGroupByTargetSource()
	byTarget := ctx.byTarget

	sortedTargets := sortedKeys(byTarget)
	for _, target := range sortedTargets {
		rrs := byTarget[target]
		constraints := make(map[string]bool)
		for _, rr := range rrs {
			if rr.File != nil {
				constraints[rr.File.BuildTag] = true
			}
		}

		// Check for mixed constraints.
		delete(constraints, "")
		if len(constraints) > 1 {
			cs := sortedKeys(constraints)
			plan.Warnings.Addf(
				"mixed build constraints (%s) going to %s",
				strings.Join(cs, "; "), target)
		}
	}
}

// detectConflicts checks for naming and movement conflicts (phase 5).
func detectConflicts(ctx *compileCtx) error {
	ix, resolved, spans := ctx.ix, ctx.resolved, ctx.spans
	resolvedGroups, opts, plan := ctx.resolvedGroups, ctx.opts, ctx.plan
	// Check movement conflicts: same group moved to two different targets.
	// Use a composite key of (group, source file path) so that declarations
	// from non-overlapping build constraints can target different files.
	type moveKey struct {
		group *mast.Group
		file  string // source file path, non-empty for build-constrained files
	}
	targetsByKey := make(map[moveKey]string)
	for _, rr := range resolved {
		mk := moveKey{group: rr.Group}
		if rr.File != nil && rr.File.BuildTag != "" {
			mk.file = rr.File.Path
		}
		if existing, ok := targetsByKey[mk]; ok {
			if existing != rr.TargetFile {
				return fmt.Errorf("conflicting moves: %s targeted to both %s and %s",
					rr.Group.Name, existing, rr.TargetFile)
			}
		}
		targetsByKey[mk] = rr.TargetFile
	}

	// Check naming conflicts in target packages.
	// Build a map of names being placed into each target directory.
	type targetEntry struct {
		name      string
		buildTag  string
		reloGroup *mast.Group
	}
	byTargetDir := make(map[string][]targetEntry)
	for _, rr := range resolved {
		if rr.Group.Kind.TravelsWithType() {
			continue
		}
		dir := rr.TargetDir
		tag := ""
		if rr.File != nil {
			tag = rr.File.BuildTag
		}
		byTargetDir[dir] = append(byTargetDir[dir], targetEntry{
			name:      rr.TargetName,
			buildTag:  tag,
			reloGroup: rr.Group,
		})
	}

	// Check for inter-relo collisions: two different relos with the same
	// TargetName going to the same directory.
	for dir, entries := range byTargetDir {
		for i := range entries {
			for j := i + 1; j < len(entries); j++ {
				if entries[i].name != entries[j].name {
					continue
				}
				if entries[i].reloGroup == entries[j].reloGroup {
					continue
				}
				if !constraintsMayOverlap(entries[i].buildTag, entries[j].buildTag) {
					continue
				}
				return fmt.Errorf("name collision: multiple declarations named %s are being moved to %s",
					entries[i].name, dir)
			}
		}
	}

	// Check against existing declarations in target packages.
	for dir, entries := range byTargetDir {
		targetPkg := ctx.cachedPkgForDir(dir)
		if targetPkg == nil {
			continue
		}

		// Build set of groups being moved out of this package, keyed by
		// their original name.  We use this to skip collision checks when
		// the existing declaration at that name is the one being moved away.
		movedFromGroups := make(map[*mast.Group]bool)
		movedFromNames := make(map[string]bool)
		for _, rr := range resolved {
			if rr.isCrossFileMove() && rr.File.Pkg == targetPkg {
				movedFromGroups[rr.Group] = true
				// Only mark the name as vacated when moving to a different
				// package. Same-package moves keep the name in the package.
				if rr.TargetDir != dir {
					movedFromNames[rr.Group.Name] = true
				}
			}
		}

		// Build a set of groups that are leaving this package entirely
		// (cross-package moves). These vacate their names.
		leavingGroups := make(map[*mast.Group]bool)
		for _, rr := range resolved {
			if rr.File != nil && rr.File.Pkg == targetPkg && rr.TargetDir != dir {
				leavingGroups[rr.Group] = true
			}
		}

		for _, entry := range entries {
			// If this entry's group is leaving the package entirely,
			// it vacates the name — skip collision check.
			if leavingGroups[entry.reloGroup] {
				continue
			}
			for _, file := range targetPkg.Files {
				// Skip declarations whose name is being vacated
				// (cross-package moves that remove the name).
				if movedFromNames[entry.name] {
					continue
				}

				if !constraintsMayOverlap(entry.buildTag, file.BuildTag) {
					continue
				}

				for _, decl := range file.Syntax.Decls {
					if !nameConflicts(decl, entry.name) {
						continue
					}
					// Don't flag the entry's own declaration as a collision.
					if movedFromGroups[entry.reloGroup] && declDefinesGroup(ix, decl, entry.reloGroup) {
						continue
					}
					return fmt.Errorf("name collision: %s already exists in %s",
						entry.name, file.Path)
				}
			}
		}
	}

	// Warn about potential circular imports for cross-package moves.
	// A cycle only forms when (a) the target package already imports the
	// source package AND (b) the move forces a new source→target import,
	// which happens only when a sibling file in the source package still
	// references the moved declaration.
	for _, rr := range resolved {
		if !rr.isCrossPackageMove() {
			continue
		}
		srcImportPath := ctx.cachedImportPath(rr.SourceDir)
		if srcImportPath == "" {
			continue
		}
		// With stubs enabled the source file keeps a forwarding alias
		// that references the target package, forcing a new import.
		stubForces := opts.stubsEnabled() && rr.Group.Kind.HasStub()
		if !stubForces && !sourceNeedsTargetImport(rr, resolved) {
			continue
		}
		if !targetImportsSource(ctx, rr.TargetFile, finalDir(rr), srcImportPath) {
			continue
		}
		plan.Warnings.AddAtf(rr, ix,
			"moving %s to %s may create a circular import: target already imports source package %s",
			rr.Group.Name, rr.TargetFile, srcImportPath)
	}

	// Warn about go:embed / go:generate directives.
	for _, rr := range resolved {
		if rr.File == nil || rr.enclosingDecl() == nil {
			continue
		}
		if hasDirective(rr.Decl, rr.File.Syntax, ix.Fset, "go:embed") {
			plan.Warnings.AddAtf(rr, ix,
				"moved decl %s has a //go:embed directive", rr.Group.Name)
		}
		if hasDirective(rr.Decl, rr.File.Syntax, ix.Fset, "go:generate") {
			plan.Warnings.AddAtf(rr, ix,
				"moved decl %s has a //go:generate directive", rr.Group.Name)
		}
	}

	// Warn about cross-package moves referencing unexported symbols or
	// symbols in package main that stay behind.
	checkCrossPkgRefs(ix, resolved, spans, resolvedGroups, plan)

	// Warn when a source file has build constraints.
	checkSourceBuildConstraints(ctx)

	return nil
}

// checkCrossPkgRefs warns when a declaration being moved cross-package
// references unexported package-scope symbols or symbols in package main
// that are not part of the move set.
func checkCrossPkgRefs(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, resolvedGroups map[*mast.Group]bool, plan *Plan) {
	for _, rr := range resolved {
		if !rr.isCrossPackageMove() {
			continue
		}

		s := spans[rr]
		if s == nil {
			continue
		}

		srcPkg := rr.File.Pkg
		if srcPkg == nil {
			continue
		}

		// Walk idents within the span to find references to the source
		// package's symbols that are staying behind.
		warned := make(map[*mast.Group]bool)
		walkRange(rr.File.Syntax, ix.Fset, s.Start, s.End, func(n ast.Node) {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return
			}
			grp := ix.Group(ident)
			if grp == nil || warned[grp] || resolvedGroups[grp] {
				return
			}
			// Only check package-scope symbols in the source package.
			if grp.Pkg != srcPkg.Path {
				return
			}
			// Only check kinds that represent package-scope declarations.
			switch grp.Kind {
			case mast.Func, mast.Const, mast.Var, mast.TypeName:
				// These are the kinds that matter.
			default:
				return
			}
			// Skip local variables and parameters — they move with the
			// declaration body. A package-scope group always has at
			// least one Def ident at file top-level (outside any FuncDecl).
			if !grp.IsPackageScope() {
				return
			}
			// Skip qualified references (pkg.Name) — those already have imports.
			for _, id := range grp.Idents {
				if id.Ident == ident && id.Qualifier != nil {
					return
				}
			}

			warned[grp] = true
			if srcPkg.Name == "main" {
				plan.Warnings.AddAtf(rr, ix,
					"moved decl %s references %q which stays in package main (main cannot be imported)",
					rr.Group.Name, grp.Name)
			} else if !token.IsExported(grp.Name) {
				plan.Warnings.AddAtf(rr, ix,
					"moved decl %s references unexported %q which is not in the move set",
					rr.Group.Name, grp.Name)
			}
		})
	}
}

// checkSourceBuildConstraints warns when a build-constrained source file's
// constraint may not be propagated to the target. The constraint is safely
// propagated when all items going to the same new target share the same
// constraint. We only warn when the constraint could be lost: the target
// file already exists, or the items mixed constrained and unconstrained
// sources (the mixed-constraints case is already covered by checkConstraints).
func checkSourceBuildConstraints(ctx *compileCtx) {
	ix, resolved, plan := ctx.ix, ctx.resolved, ctx.plan
	byTarget := ctx.byTarget

	warnedFiles := make(map[string]bool)
	for _, rr := range resolved {
		if rr.File == nil || rr.File.BuildTag == "" {
			continue
		}
		if !rr.isCrossPackageMove() {
			continue
		}
		if warnedFiles[rr.File.Path] {
			continue
		}

		// Check whether the constraint will be safely propagated.
		// For a new target file where all sources share the same
		// constraint, assemble will add the //go:build directive.
		targetExists := ix.FilesByPath[rr.TargetFile] != nil
		if !targetExists {
			allSame := true
			for _, peer := range byTarget[rr.TargetFile] {
				peerTag := ""
				if peer.File != nil {
					peerTag = peer.File.BuildTag
				}
				if peerTag != rr.File.BuildTag {
					allSame = false
					break
				}
			}
			if allSame {
				continue // constraint will be propagated
			}
		}

		warnedFiles[rr.File.Path] = true
		plan.Warnings.AddAtf(rr, ix,
			"source file %s has build constraints — moved declarations may need the same constraints in the target",
			filepath.Base(rr.File.Path))
	}
}

// constraintsMayOverlap returns true if two build constraints could coexist.
func constraintsMayOverlap(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	if a == b {
		return true
	}

	aTag := extractConstraintTag(a)
	bTag := extractConstraintTag(b)
	if aTag != "" && bTag != "" {
		// Direct negation: "linux" and "!linux" are mutually exclusive.
		if aTag == "!"+bTag || bTag == "!"+aTag {
			return false
		}

		aNeg := strings.HasPrefix(aTag, "!")
		bNeg := strings.HasPrefix(bTag, "!")
		aBase := strings.TrimPrefix(aTag, "!")
		bBase := strings.TrimPrefix(bTag, "!")

		// Resolve implies relationships (e.g. ios→darwin, android→linux).
		aResolved := resolveImplied(aBase)
		bResolved := resolveImplied(bBase)

		if aBase != bBase {
			switch {
			case !aNeg && !bNeg:
				// Two different positive exclusive tags: exclusive only if
				// neither implies the other and both are in the same
				// exclusive set.
				if aResolved != bBase && bResolved != aBase {
					if exclusiveOSTags[aBase] && exclusiveOSTags[bBase] {
						return false
					}
					if exclusiveArchTags[aBase] && exclusiveArchTags[bBase] {
						return false
					}
				}
			case aNeg && bNeg:
				// Two different negated exclusive tags: e.g. !linux and
				// !darwin both hold on FreeBSD → they overlap.
				// (conservative: return true)
			default:
				// One negated, one positive from the same exclusive set:
				// e.g. !linux and darwin → darwin implies !linux → overlap.
				// (conservative: return true)
			}
		}
	}
	return true
}

// osImplies maps GOOS values that imply another build tag.
var osImplies = map[string]string{
	"ios":     "darwin",
	"android": "linux",
}

// resolveImplied returns the tag that base implies, or "" if none.
func resolveImplied(base string) string {
	if v, ok := osImplies[base]; ok {
		return v
	}
	return ""
}

// extractConstraintTag extracts a single simple tag from a build constraint expression.
// Returns "" if the constraint is compound (contains operators or parentheses).
func extractConstraintTag(constraint string) string {
	tag := strings.TrimSpace(constraint)
	check := strings.TrimPrefix(tag, "!")
	if strings.ContainsAny(check, "&|!() ") {
		return ""
	}
	return tag
}

var exclusiveOSTags = map[string]bool{
	"linux": true, "darwin": true, "windows": true, "freebsd": true,
	"openbsd": true, "netbsd": true, "dragonfly": true, "solaris": true,
	"illumos": true, "plan9": true, "aix": true, "js": true, "wasip1": true,
	"ios": true, "android": true,
}

var exclusiveArchTags = map[string]bool{
	"amd64": true, "arm64": true, "arm": true, "386": true,
	"ppc64": true, "ppc64le": true, "mips": true, "mipsle": true,
	"mips64": true, "mips64le": true, "s390x": true, "riscv64": true,
	"wasm": true, "loong64": true,
}

// declDefinesGroup checks if a declaration's defining ident belongs to the given group.
func declDefinesGroup(ix *mast.Index, decl ast.Decl, grp *mast.Group) bool {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return ix.Group(d.Name) == grp
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if ix.Group(s.Name) == grp {
					return true
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if ix.Group(n) == grp {
						return true
					}
				}
			}
		}
	}
	return false
}

// nameConflicts checks if a declaration defines the given name.
func nameConflicts(decl ast.Decl, name string) bool {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil && d.Name.Name == name {
			return true
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.Name == name {
					return true
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// hasDirective checks if a declaration has a comment with the given directive.
func hasDirective(decl ast.Decl, file *ast.File, fset *token.FileSet, directive string) bool {
	prefix := "//" + directive

	var doc *ast.CommentGroup
	switch d := decl.(type) {
	case *ast.FuncDecl:
		doc = d.Doc
	case *ast.GenDecl:
		doc = d.Doc
	}
	if doc != nil {
		for _, c := range doc.List {
			if strings.HasPrefix(c.Text, prefix) {
				return true
			}
		}
	}

	// Check comments near the decl.
	declPos := decl.Pos()
	declEnd := decl.End()
	for _, cg := range file.Comments {
		if cg.Pos() >= declPos && cg.End() <= declEnd {
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, prefix) {
					return true
				}
			}
		}
		if cg.End() <= declPos {
			endLine := fset.Position(cg.End()).Line
			declLine := fset.Position(declPos).Line
			if declLine-endLine <= 1 {
				for _, c := range cg.List {
					if strings.HasPrefix(c.Text, prefix) {
						return true
					}
				}
			}
		}
	}
	return false
}

// sourceNeedsTargetImport reports whether moving rr will force its source
// package to import the target package. This happens when a file in the
// source package references the moved group but the file itself is not
// moving to the same target directory.
func sourceNeedsTargetImport(rr *resolvedRelo, resolved []*resolvedRelo) bool {
	if rr.File == nil {
		return false
	}
	srcPkg := rr.File.Pkg
	targetDir := rr.TargetDir

	// Build a set of source files that travel to the same target
	// directory; their references don't induce a new import.
	movingInSrcPkg := make(map[string]bool)
	for _, other := range resolved {
		if other.File == nil || other.File.Pkg != srcPkg {
			continue
		}
		if filepath.Dir(other.TargetFile) == targetDir {
			movingInSrcPkg[other.File.Path] = true
		}
	}

	for _, id := range rr.Group.Idents {
		if id.Kind != mast.Use || id.File == nil {
			continue
		}
		if id.File.Pkg != srcPkg {
			continue
		}
		if movingInSrcPkg[id.File.Path] {
			continue
		}
		return true
	}
	return false
}

// targetImportsSource reports whether any file in the package at targetDir
// imports srcImportPath. It handles both existing target files and packages
// rooted at a target directory that doesn't yet contain the target file.
func targetImportsSource(ctx *compileCtx, targetFilePath, targetDir, srcImportPath string) bool {
	if f := ctx.ix.FilesByPath[targetFilePath]; f != nil {
		for _, imp := range f.Syntax.Imports {
			if importPath(imp) == srcImportPath {
				return true
			}
		}
		return false
	}
	targetPkg := ctx.cachedPkgForDir(targetDir)
	if targetPkg == nil {
		return false
	}
	for _, f := range targetPkg.Files {
		for _, imp := range f.Syntax.Imports {
			if importPath(imp) == srcImportPath {
				return true
			}
		}
	}
	return false
}
