package relo

import (
	"go/ast"
	"go/token"
	"path/filepath"

	"github.com/loov/gorelo/mast"
)

// renameSet holds all rename edits organized by file.
type renameSet struct {
	// byFile maps file path to edits for that file.
	byFile map[string][]edit
}

// computeRenames uses mast groups to find all occurrences needing rename (phase 6).
func computeRenames(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, opts *Options, plan *Plan) *renameSet {
	rs := &renameSet{
		byFile: make(map[string][]edit),
	}

	// Build the set of groups being renamed and their new names.
	renamedGroups := make(map[*mast.Group]string)
	movedSpans := buildMovedSpanIndex(resolved, spans)

	// When stubs are enabled, track groups with cross-package moves.
	// The stubs provide backward-compatible aliases using the old name,
	// so all references (source files, same-package files, and consumer
	// packages) must keep the old name. Methods are excluded because
	// they don't get their own stubs — they follow the type alias and
	// callers need the new name.
	stubGroups := make(map[*mast.Group]bool)

	for _, rr := range resolved {
		if rr.TargetName != rr.Group.Name {
			renamedGroups[rr.Group] = rr.TargetName
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
		return rs
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
	for grp, newName := range renamedGroups {
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
			rs.byFile[id.File.Path] = append(rs.byFile[id.File.Path], edit{
				Start: off,
				End:   endOff,
				New:   newName,
			})
		}
	}

	// Deduplicate edits per file.
	for path, edits := range rs.byFile {
		rs.byFile[path] = deduplicateEdits(edits)
	}

	return rs
}

// extractedEditsResult holds the output of computeExtractedEdits.
type extractedEditsResult struct {
	edits   []edit
	imports map[string]bool
}

// computeExtractedEdits builds edits for an extracted span's text in a single
// AST walk. It handles renames (same-target groups), cross-target
// qualification (groups moving to a different target package), and
// source-stay qualification (groups staying in the source package that
// need a package prefix when the extracted code moves elsewhere).
// Edits are relative to the span's start offset.
func computeExtractedEdits(ix *mast.Index, rr *resolvedRelo, s *span, resolved []*resolvedRelo) extractedEditsResult {
	if rr.File == nil || s == nil {
		return extractedEditsResult{}
	}

	targetDir := filepath.Dir(rr.TargetFile)
	srcDir := filepath.Dir(rr.File.Path)
	isCrossPkg := srcDir != targetDir

	// Classify each resolved relo's group as either a same-target rename or
	// a cross-target reference that needs package qualification.
	type groupAction struct {
		newText string // replacement text for idents of this group
		impPath string // non-empty for cross-target (needs import)
	}
	actions := make(map[*mast.Group]*groupAction)

	// Track which groups are in the resolved set so we can detect
	// references to non-moving source-package symbols.
	resolvedGroups := make(map[*mast.Group]bool)

	for _, r := range resolved {
		resolvedGroups[r.Group] = true

		// Fields and methods travel with their parent type — treat
		// them as same-target renames so they produce plain rename
		// edits, not cross-target package-qualified references.
		rDir := filepath.Dir(r.TargetFile)
		if rDir == targetDir || r.Group.Kind.TravelsWithType() {
			// Same target — only needs a rename edit if the name changed.
			if r.TargetName != r.Group.Name {
				actions[r.Group] = &groupAction{newText: r.TargetName}
			}
			continue
		}
		// Different target — needs package-qualified reference.
		if r.File == nil {
			continue
		}
		tgtPkgPath := guessImportPath(rDir)
		if tgtPkgPath == "" {
			continue
		}
		tgtLocalName := guessImportLocalName(tgtPkgPath)
		actions[r.Group] = &groupAction{
			newText: tgtLocalName + "." + r.TargetName,
			impPath: tgtPkgPath,
		}
	}

	// Propagate renames to embedded field groups so that composite
	// literal keys (e.g., notesView{notesPage: page}) are also updated
	// when the embedded type is renamed.
	for _, r := range resolved {
		if r.Group.Kind != mast.TypeName || r.TargetName == r.Group.Name {
			continue
		}
		// Composite literal field keys are always unqualified, even
		// when the embedded type is in a different package.
		for _, fgrp := range ix.EmbeddedFieldGroups(r.Group.Name, r.Group.Pkg) {
			if _, ok := actions[fgrp]; ok {
				continue
			}
			actions[fgrp] = &groupAction{newText: r.TargetName}
		}
	}

	// For cross-package moves, compute the source package import path
	// so we can qualify references to symbols that stay in the source.
	var srcPkgPath, srcLocalName string
	if isCrossPkg {
		srcPkgPath = guessImportPath(srcDir)
		if srcPkgPath != "" {
			srcLocalName = guessImportLocalName(srcPkgPath)
		}
	}

	var edits []edit
	neededImports := make(map[string]bool)

	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		grp := ix.Group(ident)
		if grp == nil {
			return true
		}

		off := ix.Fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if off < s.Start || endOff > s.End {
			return true
		}

		if act, ok := actions[grp]; ok {
			edits = append(edits, edit{
				Start: off - s.Start,
				End:   endOff - s.Start,
				New:   act.newText,
			})
			if act.impPath != "" {
				neededImports[act.impPath] = true
			}
			return true
		}

		// Reference to a symbol not in the move set. If this is a
		// cross-package extraction and the symbol belongs to the source
		// package, qualify it with the source package name.
		if isCrossPkg && srcPkgPath != "" && !resolvedGroups[grp] &&
			!grp.Kind.TravelsWithType() && grp.IsPackageScope() {
			// Skip symbols whose definition is inside the extracted
			// span (e.g. type parameters, local declarations inside
			// a moved function) — they travel with the code.
			definedInSpan := false
			inSourcePkg := false
			for _, gid := range grp.Idents {
				if gid.Kind == mast.Def && gid.File != nil {
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
			}
			if definedInSpan {
				// Defined within extracted code — no qualification needed.
			} else if inSourcePkg {
				if token.IsExported(grp.Name) {
					edits = append(edits, edit{
						Start: off - s.Start,
						End:   endOff - s.Start,
						New:   srcLocalName + "." + grp.Name,
					})
					neededImports[srcPkgPath] = true
				}
			}
		}

		return true
	})

	return extractedEditsResult{
		edits:   deduplicateEdits(edits),
		imports: neededImports,
	}
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
			switch t := field.Type.(type) {
			case *ast.Ident:
				if t == id.Ident {
					found = true
				}
			case *ast.SelectorExpr:
				if t.Sel == id.Ident {
					found = true
				}
			case *ast.StarExpr:
				switch x := t.X.(type) {
				case *ast.Ident:
					if x == id.Ident {
						found = true
					}
				case *ast.SelectorExpr:
					if x.Sel == id.Ident {
						found = true
					}
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
