package relo

import (
	"fmt"
	"go/ast"

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
	// Also track moved groups (for cross-package qualification).
	movedGroups := make(map[*mast.Group]*resolvedRelo)
	// Track which files contain moved declarations (for filtering edits).
	movedSpans := make(map[string][]*span) // filePath -> spans being removed

	for _, rr := range resolved {
		movedGroups[rr.Group] = rr
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
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"renaming type %s to %s will also change embedded field names, which may affect serialization and reflection",
				rr.Group.Name, rr.TargetName))
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
			// For qualified references (pkg.Name), we need to handle the
			// qualifier too if the package changes.
			if id.Qualifier != nil {
				// This is a qualified reference like pkg.Name.
				// If the declaration is being moved cross-package, the
				// qualifier might need changing too, but that's handled
				// by the imports phase.
				rs.byFile[id.File.Path] = append(rs.byFile[id.File.Path], edit{
					Start: off,
					End:   endOff,
					New:   newName,
				})
			} else {
				rs.byFile[id.File.Path] = append(rs.byFile[id.File.Path], edit{
					Start: off,
					End:   endOff,
					New:   newName,
				})
			}
		}
	}

	// Deduplicate edits per file.
	for path, edits := range rs.byFile {
		rs.byFile[path] = deduplicateEdits(edits)
	}

	return rs
}

// computeExtractedRenames builds edits for an extracted span's text.
// These are relative to the span's start offset.
func computeExtractedRenames(ix *mast.Index, rr *resolvedRelo, s *span, resolved []*resolvedRelo) []edit {
	if s == nil {
		return nil
	}

	// Build rename map for all groups being renamed.
	renamedGroups := make(map[*mast.Group]string)
	for _, r := range resolved {
		if r.TargetName != r.Group.Name {
			renamedGroups[r.Group] = r.TargetName
		}
	}
	if len(renamedGroups) == 0 {
		return nil
	}

	var edits []edit

	// Walk the AST within this span to find idents that belong to renamed groups.
	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		grp := ix.Group(ident)
		if grp == nil {
			return true
		}

		newName, ok := renamedGroups[grp]
		if !ok {
			return true
		}

		off := ix.Fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if off >= s.Start && endOff <= s.End {
			edits = append(edits, edit{
				Start: off - s.Start,
				End:   endOff - s.Start,
				New:   newName,
			})
		}
		return true
	})

	return deduplicateEdits(edits)
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
			case *ast.StarExpr:
				if ti, ok := t.X.(*ast.Ident); ok && ti == id.Ident {
					found = true
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
