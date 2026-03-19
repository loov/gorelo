package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/loov/gorelo/mast"
)

// span represents the byte range of a declaration or spec to extract/remove.
type span struct {
	File       *mast.File
	Decl       ast.Decl
	Spec       ast.Spec
	Start, End int
	IsGrouped  bool   // spec from a multi-spec GenDecl
	Keyword    string // "const", "var", "type" for grouped specs
}

// computeSpans computes byte ranges for each resolved relo (phases 2-3).
func computeSpans(ix *mast.Index, resolved []*resolvedRelo, plan *Plan) map[*resolvedRelo]*span {
	spans := make(map[*resolvedRelo]*span)

	// Track const block iota warnings.
	warnedBlocks := make(map[ast.Decl]bool)

	for _, rr := range resolved {
		file := rr.File
		if file == nil {
			continue
		}

		// Find the enclosing declaration.
		decl := findEnclosingDecl(file.Syntax, rr.DefIdent.Ident)
		if decl == nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"cannot find declaration for %s in %s", rr.Group.Name, file.Path))
			continue
		}

		s := &span{
			File: file,
			Decl: decl,
		}

		switch d := decl.(type) {
		case *ast.FuncDecl:
			s.Start, s.End = declByteRange(ix.Fset, d, d.Doc, file)

		case *ast.GenDecl:
			// Find which spec contains this ident.
			spec := findSpecForIdent(d, rr.DefIdent.Ident)

			// Warn about multi-name ValueSpec partial moves.
			if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 1 {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf(
					"%s is part of a multi-name declaration; all names in the spec will be moved together",
					rr.Group.Name))
			}

			if spec != nil && len(d.Specs) > 1 {
				// Grouped spec: extract individual spec.
				s.Spec = spec
				s.IsGrouped = true
				s.Keyword = d.Tok.String()
				s.Start, s.End = specByteRange(ix.Fset, spec, file)

				// Phase 3: iota detection for const blocks.
				if d.Tok == token.CONST && !warnedBlocks[d] {
					checkIotaBlock(ix, d, rr, resolved, plan)
					warnedBlocks[d] = true
				}
			} else {
				// Single spec or whole GenDecl.
				s.Start, s.End = declByteRange(ix.Fset, d, d.Doc, file)
				if spec != nil {
					s.Spec = spec
				}
			}
		}

		spans[rr] = s
	}

	return spans
}

// findEnclosingDecl finds the ast.Decl that contains ident.
func findEnclosingDecl(file *ast.File, ident *ast.Ident) ast.Decl {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == ident {
				return d
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name == ident {
						return d
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n == ident {
							return d
						}
					}
				}
			}
		}
	}
	return nil
}

// findSpecForIdent finds which spec in a GenDecl contains ident.
func findSpecForIdent(gd *ast.GenDecl, ident *ast.Ident) ast.Spec {
	for _, spec := range gd.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name == ident {
				return s
			}
		case *ast.ValueSpec:
			for _, n := range s.Names {
				if n == ident {
					return s
				}
			}
		}
	}
	return nil
}

// declByteRange returns byte offsets for a declaration including doc comments.
func declByteRange(fset *token.FileSet, decl ast.Node, doc *ast.CommentGroup, file *mast.File) (int, int) {
	start := decl.Pos()
	end := decl.End()
	if doc != nil {
		start = doc.Pos()
	}

	src := fileContent(file)
	startOff := fset.Position(start).Offset
	endOff := fset.Position(end).Offset

	// Extend end to include trailing newlines.
	for endOff < len(src) && src[endOff] == '\n' {
		endOff++
	}

	return startOff, endOff
}

// specByteRange returns byte offsets for a single spec in a grouped block.
func specByteRange(fset *token.FileSet, spec ast.Spec, file *mast.File) (int, int) {
	specStart := spec.Pos()
	specEnd := spec.End()

	// Include spec doc comment.
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Doc != nil {
			specStart = s.Doc.Pos()
		}
	case *ast.ValueSpec:
		if s.Doc != nil {
			specStart = s.Doc.Pos()
		}
	}

	// Include trailing line comment.
	switch s := spec.(type) {
	case *ast.ValueSpec:
		if s.Comment != nil {
			specEnd = s.Comment.End()
		}
	case *ast.TypeSpec:
		if s.Comment != nil {
			specEnd = s.Comment.End()
		}
	}

	src := fileContent(file)
	startOff := fset.Position(specStart).Offset
	endOff := fset.Position(specEnd).Offset

	// Extend start to beginning of line.
	for startOff > 0 && src[startOff-1] != '\n' {
		startOff--
	}

	// Extend end to include trailing newlines.
	for endOff < len(src) && src[endOff] == '\n' {
		endOff++
	}

	return startOff, endOff
}

// checkIotaBlock warns if a const block using iota is being partially moved.
func checkIotaBlock(ix *mast.Index, gd *ast.GenDecl, rr *resolvedRelo, resolved []*resolvedRelo, plan *Plan) {
	if gd.Tok != token.CONST || len(gd.Specs) <= 1 {
		return
	}

	// Check if any spec in this block depends on iota.
	hasIota := false
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		if constSpecDependsOnIota(gd, vs) {
			hasIota = true
			break
		}
	}
	if !hasIota {
		return
	}

	// Check if all specs in the block are being moved.
	movedSpecs := make(map[ast.Spec]bool)
	for _, r := range resolved {
		if r.File == rr.File {
			decl := findEnclosingDecl(r.File.Syntax, r.DefIdent.Ident)
			if decl == gd {
				spec := findSpecForIdent(gd, r.DefIdent.Ident)
				if spec != nil {
					movedSpecs[spec] = true
				}
			}
		}
	}

	allMoved := true
	for _, spec := range gd.Specs {
		if !movedSpecs[spec] {
			allMoved = false
			break
		}
	}

	if !allMoved {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"const %s depends on iota — moving it without the full block will change its value",
			rr.Group.Name))
	}
}

// constSpecDependsOnIota checks whether a ValueSpec depends on iota.
func constSpecDependsOnIota(gd *ast.GenDecl, vs *ast.ValueSpec) bool {
	if len(vs.Values) > 0 {
		return exprListUsesIota(vs.Values)
	}
	// No explicit values — inherits from a previous spec.
	for i := len(gd.Specs) - 1; i >= 0; i-- {
		s, ok := gd.Specs[i].(*ast.ValueSpec)
		if !ok {
			continue
		}
		if s == vs {
			for j := i - 1; j >= 0; j-- {
				prev, ok := gd.Specs[j].(*ast.ValueSpec)
				if !ok {
					continue
				}
				if len(prev.Values) > 0 {
					return exprListUsesIota(prev.Values)
				}
			}
			break
		}
	}
	return false
}

// exprListUsesIota reports whether any expression references iota.
func exprListUsesIota(exprs []ast.Expr) bool {
	found := false
	for _, expr := range exprs {
		ast.Inspect(expr, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && ident.Name == "iota" {
				found = true
				return false
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// dedentBlock removes one level of tab indentation from each line.
func dedentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "\t") {
			lines[i] = line[1:]
		}
	}
	return strings.Join(lines, "\n")
}

// prependKeyword inserts a keyword before the first non-comment line.
func prependKeyword(text, keyword string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		lines[i] = keyword + " " + line
		return strings.Join(lines, "\n")
	}
	return keyword + " " + text
}
