package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
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

// movedSpanIndex maps file paths to spans being extracted to a different file.
// It provides a Contains method for checking whether a byte range falls inside
// a moved span, used by rename and consumer phases to skip idents that will be
// handled during assembly.
type movedSpanIndex map[string][]*span

// buildMovedSpanIndex builds an index of spans being moved out of their source file.
func buildMovedSpanIndex(resolved []*resolvedRelo, spans map[*resolvedRelo]*span) movedSpanIndex {
	m := make(movedSpanIndex)
	for _, rr := range resolved {
		s, ok := spans[rr]
		if !ok || s == nil || !rr.isCrossFileMove() {
			continue
		}
		m[rr.File.Path] = append(m[rr.File.Path], s)
	}
	return m
}

// Contains reports whether the byte range [off, endOff) is inside a moved span
// for the given file.
func (m movedSpanIndex) Contains(filePath string, off, endOff int) bool {
	for _, s := range m[filePath] {
		if off >= s.Start && endOff <= s.End {
			return true
		}
	}
	return false
}

// computeSpans computes byte ranges for each resolved relo (phases 2-3).
func computeSpans(ctx *compileCtx) (map[*resolvedRelo]*span, error) {
	ix, resolved, plan := ctx.ix, ctx.resolved, ctx.plan

	// First pass: cache enclosing decl on every resolvedRelo so that
	// checkIotaBlock and downstream phases can use rr.Decl directly.
	for _, rr := range resolved {
		if rr.File == nil || rr.Group.Kind == mast.Field {
			continue
		}
		rr.Decl = findEnclosingDecl(rr.File.Syntax, rr.DefIdent.Ident)
	}

	spans := make(map[*resolvedRelo]*span)
	warnedBlocks := make(map[ast.Decl]bool)

	for _, rr := range resolved {
		if rr.File == nil || rr.Group.Kind == mast.Field {
			continue
		}

		decl := rr.Decl
		if decl == nil {
			plan.Warnings.AddAtf(rr, ix,
				"cannot find declaration for %s in %s", rr.Group.Name, rr.File.Path)
			continue
		}

		s := &span{
			File: rr.File,
			Decl: decl,
		}

		switch d := decl.(type) {
		case *ast.FuncDecl:
			s.Start, s.End = declByteRange(ix.Fset, d, d.Doc, rr.File)

		case *ast.GenDecl:
			spec := findSpecForIdent(d, rr.DefIdent.Ident)

			if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 1 {
				plan.Warnings.AddAtf(rr, ix,
					"%s is part of a multi-name declaration; all names in the spec will be moved together",
					rr.Group.Name)
			}

			if spec != nil && len(d.Specs) > 1 {
				s.Spec = spec
				s.IsGrouped = true
				s.Keyword = d.Tok.String()
				s.Start, s.End = specByteRange(ix.Fset, spec, rr.File)

				if d.Tok == token.CONST && !warnedBlocks[d] {
					if err := checkIotaBlock(ix, d, rr, resolved, plan); err != nil {
						return nil, err
					}
					warnedBlocks[d] = true
				}
			} else {
				s.Start, s.End = declByteRange(ix.Fset, d, d.Doc, rr.File)
				if spec != nil {
					s.Spec = spec
				}
			}
		}

		spans[rr] = s
	}

	return spans, nil
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
					if slices.Contains(s.Names, ident) {
						return d
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
			if slices.Contains(s.Names, ident) {
				return s
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

	doc, comment := specComments(spec)
	if doc != nil {
		specStart = doc.Pos()
	}
	if comment != nil {
		specEnd = comment.End()
	}

	src := fileContent(file)
	startOff := fset.Position(specStart).Offset
	endOff := fset.Position(specEnd).Offset

	// Extend start to beginning of line (stop right after the preceding newline,
	// so this spec does not claim the newline that belongs to the previous spec).
	for startOff > 0 && src[startOff-1] != '\n' {
		startOff--
	}

	// Extend end to include exactly one trailing newline, so that only this spec
	// claims the boundary newline. The next spec's backward scan will start on
	// its own content line, preventing double blank lines when adjacent specs are
	// both being moved.
	if endOff < len(src) && src[endOff] == '\n' {
		endOff++
	}

	return startOff, endOff
}

// specComments returns the doc and trailing comment groups for a spec.
func specComments(spec ast.Spec) (doc, comment *ast.CommentGroup) {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return s.Doc, s.Comment
	case *ast.ValueSpec:
		return s.Doc, s.Comment
	}
	return nil, nil
}

// checkIotaBlock returns an error if a const block using iota is being
// partially moved, because moving individual iota-dependent specs would
// change their values or produce invalid Go.
func checkIotaBlock(ix *mast.Index, gd *ast.GenDecl, rr *resolvedRelo, resolved []*resolvedRelo, plan *Plan) error {
	if gd.Tok != token.CONST || len(gd.Specs) <= 1 {
		return nil
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
		return nil
	}

	// Check if all specs in the block are being moved.
	movedSpecs := make(map[ast.Spec]bool)
	for _, r := range resolved {
		if r.File == rr.File && r.Decl == gd {
			spec := findSpecForIdent(gd, r.DefIdent.Ident)
			if spec != nil {
				movedSpecs[spec] = true
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
		plan.Warnings.AddAtf(rr, ix,
			"const %s depends on iota — moving it without the full block will change its value",
			rr.Group.Name)
		return nil
	}

	// All specs are being moved — verify they all go to the same target file.
	targets := make(map[string]bool)
	for _, r := range resolved {
		if r.File == rr.File && r.Decl == gd {
			targets[r.TargetFile] = true
		}
	}
	if len(targets) > 1 {
		return fmt.Errorf(
			"all specs in iota-dependent const block must move to the same target file")
	}

	return nil
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
