package relo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// parseSource parses Go source text and returns the file and fset.
// Fatals on parse error.
func parseSource(t *testing.T, src string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return file, fset
}

// hasWarning reports whether any warning in the plan contains substr.
func hasWarning(plan *Plan, substr string) bool {
	for _, w := range plan.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// errContains checks whether err is non-nil and its message contains substr.
func errContains(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}

// findIdentByName finds the first ast.Ident with the given name in a file.
func findIdentByName(file *ast.File, name string) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = id
			return false
		}
		return true
	})
	return found
}
