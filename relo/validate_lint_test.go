package relo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// TestPostResolutionValidatorsTakeResolved enforces that every
// top-level validator in validate.go and conflict.go has access to
// the full resolved set. A validator that tries to make a decision
// from a per-rule view — or from whole-package state without knowing
// which relos are in flight — loses information about concurrent
// moves/renames/file-moves and has caused real regressions (d39ff98,
// 6f72da8). Requiring []*resolvedRelo in the signature makes the
// contract explicit and prevents silent drift.
//
// The lint scans the two files and flags any top-level declaration
// whose name matches `check*`, `detect*`, or `validate*` but whose
// parameter list does not include a []*resolvedRelo (or map keyed by
// *resolvedRelo). Helper predicates (e.g. constraintsMayOverlap,
// nameConflicts, hasDirective) are exempt because their names do not
// match the validator prefixes.
func TestPostResolutionValidatorsTakeResolved(t *testing.T) {
	files := []string{"validate.go", "conflict.go"}

	validatorName := regexp.MustCompile(`^(check|detect|validate)[A-Z]`)

	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if !validatorName.MatchString(fn.Name.Name) {
				continue
			}
			if !signatureHasResolvedParam(fn) {
				pos := fset.Position(fn.Pos())
				t.Errorf("%s:%d: %s is a post-resolution validator but its signature does not take []*resolvedRelo (or a map keyed by *resolvedRelo); add the parameter so the check cannot run on a partial view of the move set",
					name, pos.Line, fn.Name.Name)
			}
		}
	}
}

// signatureHasResolvedParam reports whether fn's parameter list
// includes []*resolvedRelo or map[*resolvedRelo]*span (or similar
// map keyed by *resolvedRelo).
func signatureHasResolvedParam(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		if typeRefersToResolved(field.Type) {
			return true
		}
	}
	return false
}

func typeRefersToResolved(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.ArrayType:
		// []*resolvedRelo
		return exprString(t.Elt) == "*resolvedRelo"
	case *ast.MapType:
		// map[*resolvedRelo]T
		return exprString(t.Key) == "*resolvedRelo"
	}
	return false
}

func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	}
	// Fallback: render a best-effort textual form.
	var b strings.Builder
	astFormat(&b, expr)
	return b.String()
}

func astFormat(b *strings.Builder, expr ast.Expr) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		b.WriteString("*")
		astFormat(b, t.X)
	case *ast.Ident:
		b.WriteString(t.Name)
	case *ast.SelectorExpr:
		astFormat(b, t.X)
		b.WriteString(".")
		b.WriteString(t.Sel.Name)
	}
}
