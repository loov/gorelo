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
func computeRenames(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, opts *Options, plan *Plan, edits *ed.Plan) {
	// Build the set of groups being renamed and their new names.
	renamedGroups := make(map[*mast.Group]string)
	movedSpans := buildMovedSpanIndex(resolved, spans)

	// Groups with detach/attach are handled by the detach phase,
	// which integrates the rename into its structural edits.
	detachGroups := make(map[*mast.Group]bool)
	for _, rr := range resolved {
		if rr.Relo.Detach || rr.Relo.MethodOf != "" {
			detachGroups[rr.Group] = true
		}
	}

	// When stubs are enabled, track groups with cross-package moves.
	// The stubs provide backward-compatible aliases using the old name,
	// so all references (source files, same-package files, and consumer
	// packages) must keep the old name. Methods are excluded because
	// they don't get their own stubs — they follow the type alias and
	// callers need the new name.
	stubGroups := make(map[*mast.Group]bool)

	// Cross-package-moved groups: their use-sites are emitted by
	// computeConsumerEdits with full package qualification. computeRenames
	// must skip them to avoid emitting overlapping plain-rename Replace
	// primitives that would conflict with the consumer's qualified ones.
	crossPkgMovedGroups := make(map[*mast.Group]bool)

	for _, rr := range resolved {
		if rr.TargetName != rr.Group.Name {
			renamedGroups[rr.Group] = rr.TargetName
		}

		if rr.File != nil && !rr.Relo.Detach && rr.Relo.MethodOf == "" {
			srcDir := filepath.Dir(rr.File.Path)
			tgtDir := filepath.Dir(rr.TargetFile)
			if srcDir != tgtDir {
				crossPkgMovedGroups[rr.Group] = true
			}
		}

		if opts.stubsEnabled() && rr.isCrossFileMove() {
			srcDir := filepath.Dir(rr.File.Path)
			tgtDir := filepath.Dir(rr.TargetFile)
			if srcDir != tgtDir && rr.Group.Kind.HasStub() {
				stubGroups[rr.Group] = true
			}
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
			// Find embedded field groups with the same name and package.
			// These contain composite literal keys and selector idents
			// that must be renamed alongside the type.
			for _, fgrp := range ix.EmbeddedFieldGroups(rr.Group.Name, rr.Group.Pkg) {
				renamedGroups[fgrp] = rr.TargetName
				// Propagate stub status: with stubs the alias preserves
				// the old embedded field name.
				if stubGroups[rr.Group] {
					stubGroups[fgrp] = true
				}
			}
		}
	}

	// For each renamed group, iterate through all its idents and create edits.
	// Skip groups handled by the detach/attach phase. Non-method/field
	// cross-package-moved groups have their use-sites handled exclusively
	// by computeConsumerEdits (whose qualified Replace overlaps the plain
	// rename); method/field groups still need this loop because consumer
	// only handles the in-target-package case for them.
	for grp, newName := range renamedGroups {
		if detachGroups[grp] {
			continue
		}
		if crossPkgMovedGroups[grp] && !grp.Kind.TravelsWithType() {
			continue
		}
		for _, id := range grp.Idents {
			if id.File == nil {
				continue
			}

			off := ix.Fset.Position(id.Ident.Pos()).Offset
			endOff := off + len(id.Ident.Name)

			// Inside a moved span — will be handled during assembly.
			if movedSpans.Contains(id.File.Path, off, endOff) {
				continue
			}

			// When stubs are enabled, the source package gets an alias
			// using the old name.  All references (source files, same-
			// package files, and consumer packages) must keep the old
			// name so they resolve through the alias.
			if stubGroups[grp] {
				continue
			}

			// This is a use-site in non-moved code that needs renaming.
			// For qualified references (pkg.Name), the qualifier might
			// need changing too, but that's handled by the imports phase.
			edits.Replace(ed.Span{Path: id.File.Path, Start: off, End: endOff}, newName, "rename-use")
		}
	}
}

// rewriteSpanQualifiers walks the moved span in rr's source file once
// and emits span-relative edits that transform every package qualifier
// and every moved-group ident reference to its destination
// representation. As a side effect it registers the destination imports
// the rewrites need on the importSet — addImportEntry resolves
// collisions against the destination's existing+queued imports, so the
// alias used in the emitted edits matches what applyImportsPass will
// actually install.
//
// Subsumes the trio computeExtractedEdits + collectSelfImportEdits +
// computeImportAliasEdits with one ast.Inspect.
func rewriteSpanQualifiers(ix *mast.Index, rr *resolvedRelo, s *span, resolved []*resolvedRelo, imports *importSet) []edit {
	if rr.File == nil || s == nil {
		return nil
	}

	targetPath := rr.TargetFile
	targetDir := filepath.Dir(targetPath)
	targetImportPath := guessImportPath(targetDir)
	srcDir := filepath.Dir(rr.File.Path)
	isCrossPkg := srcDir != targetDir

	// Per-group action lookup.
	type groupAction struct {
		// For same-target / TravelsWithType groups: plain rename to
		// targetName. For cross-target groups: qualified
		// `<destLocal>.<targetName>` where the qualifier is resolved
		// from imports lazily so any addImportEntry collision alias
		// is reflected.
		targetName  string
		impPath     string // non-empty → cross-target
		crossTarget bool
	}
	actions := make(map[*mast.Group]*groupAction)
	resolvedGroups := make(map[*mast.Group]bool)

	for _, r := range resolved {
		resolvedGroups[r.Group] = true
		if r.Relo.Detach || r.Relo.MethodOf != "" {
			continue
		}
		rDir := filepath.Dir(r.TargetFile)
		if rDir == targetDir || r.Group.Kind.TravelsWithType() {
			actions[r.Group] = &groupAction{targetName: r.TargetName}
			continue
		}
		rImpPath := guessImportPath(rDir)
		if rImpPath == "" {
			continue
		}
		actions[r.Group] = &groupAction{
			targetName:  r.TargetName,
			impPath:     rImpPath,
			crossTarget: true,
		}
	}

	// Propagate type renames to embedded field groups so composite
	// literal keys (notesView{notesPage: page}) get rewritten.
	for _, r := range resolved {
		if r.Group.Kind != mast.TypeName || r.TargetName == r.Group.Name {
			continue
		}
		for _, fgrp := range ix.EmbeddedFieldGroups(r.Group.Name, r.Group.Pkg) {
			if _, ok := actions[fgrp]; ok {
				continue
			}
			actions[fgrp] = &groupAction{targetName: r.TargetName}
		}
	}

	var srcImportPath string
	if isCrossPkg {
		srcImportPath = guessImportPath(srcDir)
	}

	// registerImport queues impPath in destination's importChange.
	// addImportEntry handles real-pkg-name aliasing and collision
	// resolution against the destination's existing+queued imports.
	registered := make(map[string]bool)
	registerImport := func(impPath string) {
		if impPath == "" || registered[impPath] {
			return
		}
		registered[impPath] = true
		addImportEntry(imports, ix, targetPath, importEntry{Path: impPath})
	}

	// Pre-register cross-target imports in path-sorted order so
	// addImportEntry's collision resolution yields deterministic
	// alias assignments independent of walk order. srcImportPath is
	// lazily registered only when an actual cross-pkg-stay edit needs
	// it (otherwise we'd add a spurious source-package import that
	// reformats the destination's import block).
	{
		seen := make(map[string]bool)
		var sortedImpPaths []string
		for _, act := range actions {
			if act.crossTarget && !seen[act.impPath] {
				seen[act.impPath] = true
				sortedImpPaths = append(sortedImpPaths, act.impPath)
			}
		}
		sort.Strings(sortedImpPaths)
		for _, impPath := range sortedImpPaths {
			registerImport(impPath)
		}
	}

	// destLocal returns the local name destination uses for impPath,
	// honoring any alias addImportEntry assigned. Falls back to the
	// package's real name from ix when impPath isn't (yet) recorded.
	destLocal := func(impPath string) string {
		if ic := imports.byFile[targetPath]; ic != nil {
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
		for _, pkg := range ix.Pkgs {
			if pkg.Path == impPath {
				return pkg.Name
			}
		}
		return guessImportLocalName(impPath)
	}

	// Source-file imports keyed by their local name as referenced in
	// source code.
	importByLocal := make(map[string]string)
	for _, imp := range rr.File.Syntax.Imports {
		impPath := importPath(imp)
		importByLocal[importLocalName(imp, impPath)] = impPath
	}

	// Pre-pass: for every SelectorExpr X.Sel where Sel is in a moved
	// group, mark X so the SelectorExpr qualifier-rewrite handler
	// skips it (the Ident handler for Sel will extend left to swallow
	// X. itself).
	handledQualifier := make(map[*ast.Ident]bool)
	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qid, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		grp := ix.Group(sel.Sel)
		if grp == nil || actions[grp] == nil {
			return true
		}
		handledQualifier[qid] = true
		return true
	})

	inSpan := func(start, end int) bool {
		return start >= s.Start && end <= s.End
	}

	var edits []edit
	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		// SelectorExpr handler: rewrite import-qualifier idents that
		// resolve to destination's package (strip) or to a destination
		// import with a different local name (rewrite).
		if sel, ok := n.(*ast.SelectorExpr); ok {
			qid, ok := sel.X.(*ast.Ident)
			if !ok || handledQualifier[qid] {
				return true
			}
			qOff := ix.Fset.Position(qid.Pos()).Offset
			sOff := ix.Fset.Position(sel.Sel.Pos()).Offset
			if !inSpan(qOff, sOff) {
				return true
			}
			impPath, isImport := importByLocal[qid.Name]
			if !isImport {
				return true
			}
			if targetImportPath != "" && impPath == targetImportPath {
				edits = append(edits, edit{
					Start: qOff - s.Start,
					End:   sOff - s.Start,
					New:   "",
				})
				return true
			}
			destName := destLocal(impPath)
			if destName == qid.Name {
				return true
			}
			edits = append(edits, edit{
				Start: qOff - s.Start,
				End:   qOff - s.Start + len(qid.Name),
				New:   destName,
			})
			return true
		}

		// Ident handler: moved-group rewrites + cross-pkg-stay
		// qualification of references to non-moved source-pkg symbols.
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		off := ix.Fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if !inSpan(off, endOff) {
			return true
		}
		grp := ix.Group(ident)
		if grp == nil {
			return true
		}

		if act, ok := actions[grp]; ok {
			newText := act.targetName
			if act.crossTarget {
				newText = destLocal(act.impPath) + "." + act.targetName
			}
			editStart := off
			for _, gid := range grp.Idents {
				if gid.Ident == ident && gid.Qualifier != nil {
					qOff := ix.Fset.Position(gid.Qualifier.Pos()).Offset
					if qOff >= s.Start {
						editStart = qOff
					}
					break
				}
			}
			if editStart == off && newText == ident.Name {
				return true
			}
			edits = append(edits, edit{
				Start: editStart - s.Start,
				End:   endOff - s.Start,
				New:   newText,
			})
			return true
		}

		// Cross-pkg extraction: bare reference to a non-moved
		// package-scope source-pkg symbol must be qualified.
		if !isCrossPkg || srcImportPath == "" || resolvedGroups[grp] ||
			grp.Kind.TravelsWithType() || !grp.IsPackageScope() {
			return true
		}
		definedInSpan := false
		inSourcePkg := false
		for _, gid := range grp.Idents {
			if gid.Kind != mast.Def || gid.File == nil {
				continue
			}
			defOff := ix.Fset.Position(gid.Ident.Pos()).Offset
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
		registerImport(srcImportPath)
		edits = append(edits, edit{
			Start: off - s.Start,
			End:   endOff - s.Start,
			New:   destLocal(srcImportPath) + "." + grp.Name,
		})
		return true
	})

	return edits
}

// resolvedForGroup finds the resolvedRelo for a given group.
func resolvedForGroup(resolved []*resolvedRelo, grp *mast.Group) *resolvedRelo {
	for _, r := range resolved {
		if r.Group == grp {
			return r
		}
	}
	return nil
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
