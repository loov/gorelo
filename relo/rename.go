package relo

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// computeRenames uses mast groups to find all occurrences needing rename
// (phase 6) and emits the corresponding Replace primitives onto edits.
// computeRenames emits Replace primitives for every use-site ident of
// renamed groups. It only touches the ident region [identStart, identEnd);
// qualifier changes and structural edits (detach, consumer) are handled
// by their own passes on non-overlapping byte regions.
func computeRenames(ctx *compileCtx) {
	ix, resolved := ctx.ix, ctx.resolved
	movedSpans, opts, plan, edits := ctx.movedSpans, ctx.opts, ctx.plan, ctx.edits
	renamedGroups := make(map[*mast.Group]string)

	// When stubs are enabled, track groups with cross-package moves.
	// The stubs provide backward-compatible aliases using the old name,
	// so all references must keep the old name.
	stubGroups := make(map[*mast.Group]bool)

	for _, rr := range resolved {
		if rr.TargetName != rr.Group.Name {
			renamedGroups[rr.Group] = rr.TargetName
		}

		if opts.stubsEnabled() && rr.isCrossPackageMove() && rr.Group.Kind.HasStub() {
			stubGroups[rr.Group] = true
		}
	}

	if len(renamedGroups) == 0 {
		return
	}

	// Warn about type renames that may affect embedded field names,
	// and propagate the rename to the embedded field groups so that
	// composite literal keys and selectors are also updated.
	for _, rr := range resolved {
		if rr.Group.Kind != mast.TypeName || rr.TargetName == rr.Group.Name {
			continue
		}
		if typeHasEmbeddedUses(ix, rr.Group) {
			plan.Warnings.AddAtf(rr, ix,
				"renaming type %s to %s will also change embedded field names, which may affect serialization and reflection",
				rr.Group.Name, rr.TargetName)
			for _, fgrp := range ix.EmbeddedFieldGroups(rr.Group.Name, rr.Group.Pkg) {
				renamedGroups[fgrp] = rr.TargetName
				if stubGroups[rr.Group] {
					stubGroups[fgrp] = true
				}
			}
		}
	}

	for grp, newName := range renamedGroups {
		if stubGroups[grp] {
			continue
		}
		for _, id := range grp.Idents {
			if id.File == nil {
				continue
			}

			off := ix.Fset.Position(id.Ident.Pos()).Offset
			endOff := off + len(id.Ident.Name)

			if movedSpans.Contains(id.File.Path, off, endOff) {
				continue
			}

			edits.Replace(ed.Span{Path: id.File.Path, Start: off, End: endOff}, newName, "rename-use")
		}
	}
}

// spanRewriter holds precomputed state for rewriting qualifiers and
// ident references within a moved span.
type spanRewriter struct {
	ix             *mast.Index
	rr             *resolvedRelo
	s              *span
	resolvedGroups map[*mast.Group]bool
	imports        *importSet

	targetPath       string
	targetImportPath string
	srcImportPath    string
	isCrossPkg       bool
	actions          map[*mast.Group]*groupAction

	// importByLocal maps source-file import local names to import paths.
	importByLocal map[string]string

	// specByPath maps import path to import spec for the source file.
	specByPath map[string]*ast.ImportSpec

	// registered tracks imports already queued to avoid duplicates.
	registered map[string]bool
}

// groupAction describes how a moved group's references should be
// rewritten in the destination.
type groupAction struct {
	targetName string
	ref        qualifyEdit
}

// qualifiedName returns the fully-qualified text for this action,
// using sw.destLocal to resolve any import alias at the destination.
func (act *groupAction) qualifiedName(sw *spanRewriter) string {
	if act.ref.LocalRef {
		return act.targetName
	}
	return sw.destLocal(act.ref.ImportPath) + "." + act.targetName
}

// newSpanRewriter builds the action table and registers cross-target
// destination imports. The returned spanRewriter is ready for the
// single-pass rewrite walk in rewriteSpanQualifiers.
func newSpanRewriter(ctx *compileCtx, rr *resolvedRelo, s *span) *spanRewriter {
	ix, resolved, resolvedGroups, imports := ctx.ix, ctx.resolved, ctx.resolvedGroups, ctx.imports
	targetPath := rr.TargetFile
	targetDir := filepath.Dir(targetPath)

	sw := &spanRewriter{
		ix:               ix,
		rr:               rr,
		s:                s,
		resolvedGroups:   resolvedGroups,
		imports:          imports,
		targetPath:       targetPath,
		targetImportPath: ctx.cachedImportPath(targetDir),
		isCrossPkg:       rr.isCrossPackageMove(),
		actions:          make(map[*mast.Group]*groupAction),
		importByLocal:    make(map[string]string),
		specByPath:       make(map[string]*ast.ImportSpec),
		registered:       make(map[string]bool),
	}

	// Build per-group action lookup.
	for _, r := range resolved {
		rDir := filepath.Dir(r.TargetFile)
		if r.Group.Kind.TravelsWithType() {
			sw.actions[r.Group] = &groupAction{targetName: r.TargetName, ref: qualifyEdit{LocalRef: true}}
			continue
		}
		ref := ctx.classifyRef(rDir, targetDir)
		if !ref.LocalRef && ref.ImportPath == "" {
			continue
		}
		sw.actions[r.Group] = &groupAction{targetName: r.TargetName, ref: ref}
	}

	// Propagate type renames to embedded field groups.
	for _, r := range resolved {
		if r.Group.Kind != mast.TypeName || r.TargetName == r.Group.Name {
			continue
		}
		for _, fgrp := range ix.EmbeddedFieldGroups(r.Group.Name, r.Group.Pkg) {
			if _, ok := sw.actions[fgrp]; ok {
				continue
			}
			sw.actions[fgrp] = &groupAction{targetName: r.TargetName}
		}
	}

	if sw.isCrossPkg {
		sw.srcImportPath = ctx.cachedImportPath(rr.SourceDir)
	}

	// Pre-register cross-target imports in path-sorted order so
	// collision resolution is deterministic.
	{
		seen := make(map[string]bool)
		var sortedImpPaths []string
		for _, act := range sw.actions {
			if !act.ref.LocalRef && act.ref.ImportPath != "" && !seen[act.ref.ImportPath] {
				seen[act.ref.ImportPath] = true
				sortedImpPaths = append(sortedImpPaths, act.ref.ImportPath)
			}
		}
		sort.Strings(sortedImpPaths)
		for _, impPath := range sortedImpPaths {
			sw.registerImport(impPath)
		}
	}

	// Build source-file import maps.
	for _, imp := range rr.File.Syntax.Imports {
		impPath := importPath(imp)
		sw.importByLocal[importLocalName(imp, impPath)] = impPath
		sw.specByPath[impPath] = imp
	}

	return sw
}

func (sw *spanRewriter) registerImport(impPath string) {
	if impPath == "" || sw.registered[impPath] {
		return
	}
	sw.registered[impPath] = true
	addImportEntry(sw.imports, sw.ix, sw.targetPath, importEntry{Path: impPath})
}

// registerExternalImport registers an import discovered in the span
// at the destination file, preserving any alias from the source file.
func (sw *spanRewriter) registerExternalImport(impPath string) {
	spec := sw.specByPath[impPath]
	if spec == nil {
		return
	}
	localName := importLocalName(spec, impPath)
	if localName == "." || localName == "_" {
		return
	}
	entry := importEntry{Path: impPath}
	if spec.Name != nil && spec.Name.Name != guessImportLocalName(impPath) {
		entry.Alias = spec.Name.Name
	}
	addImportEntry(sw.imports, sw.ix, sw.targetPath, entry)
}

// destLocal returns the local name the destination uses for impPath.
func (sw *spanRewriter) destLocal(impPath string) string {
	if ic := sw.imports.byFile[sw.targetPath]; ic != nil {
		for _, e := range ic.Add {
			if e.Path == impPath && e.Alias != "" {
				return e.Alias
			}
		}
		for _, e := range ic.Existing {
			if e.Path == impPath && e.Alias != "" {
				return e.Alias
			}
		}
	}
	for _, pkg := range sw.ix.Pkgs {
		if pkg.Path == impPath {
			return pkg.Name
		}
	}
	return guessImportLocalName(impPath)
}

// rewriteSpanQualifiers walks the moved span in a single pass and
// emits edit primitives that transform package qualifiers and ident
// references to their destination representation. It also registers
// destination imports on the importSet.
//
// SelectorExpr nodes are handled first (parent before children in
// ast.Inspect). When sel.Sel has a group action and sel.X is its
// mast qualifier, the SelectorExpr handler emits the full rewrite
// and returns false to skip children. Otherwise, import-qualifier
// rewrites are handled here and children are visited normally.
func rewriteSpanQualifiers(ctx *compileCtx, rr *resolvedRelo, s *span, origin string) {
	if rr.File == nil || s == nil {
		return
	}

	sw := newSpanRewriter(ctx, rr, s)
	ix, plan := ctx.ix, ctx.edits
	srcPath := rr.File.Path
	fset := ix.Fset

	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		// SelectorExpr handler: handles qualified references (pkg.Name).
		if sel, ok := n.(*ast.SelectorExpr); ok {
			qid, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			qOff := fset.Position(qid.Pos()).Offset
			selOff := fset.Position(sel.Sel.Pos()).Offset
			selEnd := selOff + len(sel.Sel.Name)
			if qOff < s.Start || selEnd > s.End {
				return true
			}

			// When Sel has a group action and X is its mast qualifier,
			// emit the full rewrite and skip children.
			if grp := ix.Group(sel.Sel); grp != nil {
				if act, ok := sw.actions[grp]; ok {
					for _, gid := range grp.Idents {
						if gid.Ident == sel.Sel && gid.Qualifier == qid {
							newText := act.qualifiedName(sw)
							emitEdit(plan, srcPath, qOff, selEnd, newText, origin)
							return false
						}
					}
				}
			}

			// Import-qualifier handling: strip self-imports, rewrite
			// aliases, and register used external imports.
			impPath, isImport := sw.importByLocal[qid.Name]
			if !isImport {
				return true
			}
			if impPath != sw.targetImportPath {
				sw.registerExternalImport(impPath)
			}
			if sw.targetImportPath != "" && impPath == sw.targetImportPath {
				emitEdit(plan, srcPath, qOff, selOff, "", origin)
				return true
			}
			destName := sw.destLocal(impPath)
			if destName != qid.Name {
				emitEdit(plan, srcPath, qOff, qOff+len(qid.Name), destName, origin)
			}
			return true
		}

		// Ident handler: bare (unqualified) group rewrites +
		// cross-pkg-stay qualification.
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		off := fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if off < s.Start || endOff > s.End {
			return true
		}
		grp := ix.Group(ident)
		if grp == nil {
			return true
		}

		if act, ok := sw.actions[grp]; ok {
			newText := act.qualifiedName(sw)
			if newText == ident.Name {
				return true
			}
			emitEdit(plan, srcPath, off, endOff, newText, origin)
			return true
		}

		// Cross-pkg extraction: bare reference to a non-moved
		// package-scope source-pkg symbol must be qualified.
		if !sw.isCrossPkg || sw.srcImportPath == "" || sw.resolvedGroups[grp] ||
			grp.Kind.TravelsWithType() || !grp.IsPackageScope() {
			return true
		}
		definedInSpan := false
		inSourcePkg := false
		for _, gid := range grp.Idents {
			if gid.Kind != mast.Def || gid.File == nil {
				continue
			}
			defOff := fset.Position(gid.Ident.Pos()).Offset
			defEnd := defOff + len(gid.Ident.Name)
			if gid.File.Path == rr.File.Path && defOff >= s.Start && defEnd <= s.End {
				definedInSpan = true
				break
			}
			if gid.File.Pkg == rr.File.Pkg {
				inSourcePkg = true
			}
		}
		if definedInSpan || !inSourcePkg || !token.IsExported(grp.Name) {
			return true
		}
		sw.registerImport(sw.srcImportPath)
		emitEdit(plan, srcPath, off, endOff, sw.destLocal(sw.srcImportPath)+"."+grp.Name, origin)
		return true
	})
}

// typeHasEmbeddedUses checks if a TypeName group has any Use idents
// that appear as embedded fields in struct declarations.
func typeHasEmbeddedUses(ix *mast.Index, grp *mast.Group) bool {
	for _, id := range grp.Idents {
		if id.Kind != mast.Use || id.File == nil {
			continue
		}
		// Walk the file to check if this ident is used as an anonymous
		// (embedded) field in a struct type.
		found := false
		ast.Inspect(id.File.Syntax, func(n ast.Node) bool {
			if found {
				return false
			}
			field, ok := n.(*ast.Field)
			if !ok {
				return true
			}
			// An embedded field has no explicit names.
			if len(field.Names) > 0 {
				return true
			}
			// Check if the field type is our ident.
			if embeddedFieldIdent(field.Type) == id.Ident {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// embeddedFieldIdent returns the type name ident for an embedded field
// type expression, handling plain idents, selector expressions, pointer
// types, and generic instantiations (IndexExpr / IndexListExpr).
func embeddedFieldIdent(expr ast.Expr) *ast.Ident {
	// Unwrap pointer.
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	// Unwrap generic instantiation: T[X] or T[X, Y].
	if idx, ok := expr.(*ast.IndexExpr); ok {
		expr = idx.X
	}
	if idx, ok := expr.(*ast.IndexListExpr); ok {
		expr = idx.X
	}
	// Now extract the ident.
	switch t := expr.(type) {
	case *ast.Ident:
		return t
	case *ast.SelectorExpr:
		return t.Sel
	}
	return nil
}
