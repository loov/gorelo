package relo

import (
	"go/ast"
	"path/filepath"

	"github.com/loov/gorelo/mast"
)

// renameSet holds all rename edits organized by file.
type renameSet struct {
	// byFile maps file path to edits for that file.
	byFile map[string][]edit
}

// computeRenames uses mast groups to find all occurrences needing rename (phase 6).
func computeRenames(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, plan *Plan) *renameSet {
	rs := &renameSet{
		byFile: make(map[string][]edit),
	}

	// Build the set of groups being renamed and their new names.
	renamedGroups := make(map[*mast.Group]string)
	// Track which files contain moved declarations (for filtering edits).
	movedSpans := make(map[string][]*span) // filePath -> spans being removed

	for _, rr := range resolved {
		if rr.TargetName != rr.Group.Name {
			renamedGroups[rr.Group] = rr.TargetName
		}
		// Only track as "moved" if the declaration is actually being extracted
		// to a different file. Same-file renames don't remove the span.
		if s, ok := spans[rr]; ok && s != nil && rr.File != nil && rr.TargetFile != rr.File.Path {
			movedSpans[rr.File.Path] = append(movedSpans[rr.File.Path], s)
		}
	}

	if len(renamedGroups) == 0 {
		return rs
	}

	// Warn about type renames that may affect embedded field names.
	for _, rr := range resolved {
		if rr.Group.Kind != mast.TypeName || rr.TargetName == rr.Group.Name {
			continue
		}
		if typeHasEmbeddedUses(ix, rr.Group) {
			plan.Warnings.AddAtf(rr, ix,
				"renaming type %s to %s will also change embedded field names, which may affect serialization and reflection",
				rr.Group.Name, rr.TargetName)
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

			// Check if this ident is inside a moved span — if so, the edit
			// will be applied to the extracted text in the target file.
			inMovedSpan := false
			for _, s := range movedSpans[id.File.Path] {
				if off >= s.Start && endOff <= s.End {
					inMovedSpan = true
					break
				}
			}

			if inMovedSpan {
				// Will be handled during assembly when extracting text.
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
// AST walk. It handles both renames (same-target groups) and cross-target
// qualification (groups moving to a different target package). Edits are
// relative to the span's start offset.
func computeExtractedEdits(ix *mast.Index, rr *resolvedRelo, s *span, resolved []*resolvedRelo) extractedEditsResult {
	if rr.File == nil || s == nil {
		return extractedEditsResult{}
	}

	targetDir := filepath.Dir(rr.TargetFile)

	// Classify each resolved relo's group as either a same-target rename or
	// a cross-target reference that needs package qualification.
	type groupAction struct {
		newText string // replacement text for idents of this group
		impPath string // non-empty for cross-target (needs import)
	}
	actions := make(map[*mast.Group]*groupAction)

	for _, r := range resolved {
		rDir := filepath.Dir(r.TargetFile)
		if rDir == targetDir {
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
		srcDir := filepath.Dir(r.File.Path)
		if srcDir == targetDir {
			continue // declaration is already in our target package
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
	if len(actions) == 0 {
		return extractedEditsResult{}
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
		act, ok := actions[grp]
		if !ok {
			return true
		}

		off := ix.Fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if off >= s.Start && endOff <= s.End {
			edits = append(edits, edit{
				Start: off - s.Start,
				End:   endOff - s.Start,
				New:   act.newText,
			})
			if act.impPath != "" {
				neededImports[act.impPath] = true
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
