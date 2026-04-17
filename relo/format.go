package relo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// addImports adds all entries to src in one pass. It parses src once
// to check for existing imports, collects warnings for alias
// mismatches, and inserts all new import lines with a single text
// edit. Entries should be pre-sorted by path for deterministic output.
func addImports(src string, entries []importEntry) (string, Warnings) {
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, "", src, parser.ImportsOnly)

	existing := make(map[string]string)
	if file != nil {
		for _, imp := range file.Imports {
			p := importPath(imp)
			alias := ""
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			existing[p] = alias
		}
	}

	var newLines []string
	var warnings Warnings
	for _, entry := range entries {
		if existingAlias, has := existing[entry.Path]; has {
			expected := entry.Alias
			if expected == "" {
				expected = guessImportLocalName(entry.Path)
			}
			effective := existingAlias
			if effective == "" {
				effective = guessImportLocalName(entry.Path)
			}
			if effective != expected {
				warnings.Addf("import %s exists with alias %q but moved code expects %q",
					strconv.Quote(entry.Path), effective, expected)
			}
			continue
		}
		line := "\t"
		if entry.Alias != "" {
			line += entry.Alias + " "
		}
		line += strconv.Quote(entry.Path)
		newLines = append(newLines, line)
	}

	if len(newLines) == 0 {
		return src, warnings
	}

	lines := strings.Split(src, "\n")

	// Look for grouped import block — insert before closing ")".
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "import (" {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == ")" {
					out := make([]string, 0, len(lines)+len(newLines))
					out = append(out, lines[:j]...)
					out = append(out, newLines...)
					out = append(out, lines[j:]...)
					return strings.Join(out, "\n"), warnings
				}
			}
			out := make([]string, 0, len(lines)+len(newLines))
			out = append(out, lines[:i+1]...)
			out = append(out, newLines...)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), warnings
		}
	}

	// Look for single-line import — convert to grouped block.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") && !strings.HasPrefix(trimmed, "import (") {
			existingImport := "\t" + strings.TrimPrefix(trimmed, "import ")
			out := make([]string, 0, len(lines)+len(newLines)+3)
			out = append(out, lines[:i]...)
			out = append(out, "import (")
			out = append(out, existingImport)
			out = append(out, newLines...)
			out = append(out, ")")
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), warnings
		}
	}

	// No import — add after package clause.
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "package ") {
			out := make([]string, 0, len(lines)+len(newLines)+4)
			out = append(out, lines[:i+1]...)
			out = append(out, "")
			out = append(out, "import (")
			out = append(out, newLines...)
			out = append(out, ")")
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), warnings
		}
	}

	return src, warnings
}

// removeUnusedImportsText re-parses src and removes any imports whose
// local name is not referenced.
func removeUnusedImportsText(src string) string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return src
	}
	if len(file.Imports) == 0 {
		return src
	}

	usedPkgs := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok {
			usedPkgs[ident.Name] = true
		}
		return true
	})

	var unusedPaths []string
	for _, imp := range file.Imports {
		impPath := importPath(imp)
		localName := importLocalName(imp, impPath)
		if localName == "_" || localName == "." {
			continue
		}
		if !usedPkgs[localName] {
			unusedPaths = append(unusedPaths, imp.Path.Value)
		}
	}

	if len(unusedPaths) == 0 {
		return src
	}

	unusedSet := make(map[string]bool)
	for _, p := range unusedPaths {
		unusedSet[p] = true
	}

	removeLines := make(map[int]bool)
	for _, imp := range file.Imports {
		if unusedSet[imp.Path.Value] {
			removeLines[fset.Position(imp.Pos()).Line] = true
			if imp.Doc != nil {
				startLine := fset.Position(imp.Doc.Pos()).Line
				endLine := fset.Position(imp.Doc.End()).Line
				for l := startLine; l <= endLine; l++ {
					removeLines[l] = true
				}
			}
		}
	}

	lines := strings.Split(src, "\n")
	var out []string
	for i, line := range lines {
		if !removeLines[i+1] {
			out = append(out, line)
		}
	}

	result := strings.Join(out, "\n")
	result = removeEmptyDeclBlocks(result)
	return result
}

// removeEmptyDeclBlocks removes empty declaration blocks like
// "import (\n)" that may be left behind after pruning unused imports.
func removeEmptyDeclBlocks(src string) string {
	lines := strings.Split(src, "\n")
	for _, keyword := range []string{"import", "const", "var", "type"} {
		prefix := keyword + " ("
		var out []string
		i := 0
		for i < len(lines) {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == prefix {
				// Scan forward for the closing ")".
				j := i + 1
				empty := true
				for j < len(lines) {
					inner := strings.TrimSpace(lines[j])
					if inner == ")" {
						break
					}
					if inner != "" {
						empty = false
					}
					j++
				}
				if empty && j < len(lines) && strings.TrimSpace(lines[j]) == ")" {
					i = j + 1
					for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
						i++
					}
					continue
				}
			}
			out = append(out, lines[i])
			i++
		}
		lines = out
	}
	return strings.Join(lines, "\n")
}

// cleanBlankLines collapses runs of more than two consecutive blank
// lines down to two.
func cleanBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blankCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankCount++
			if blankCount <= 2 {
				out = append(out, line)
			}
		} else {
			blankCount = 0
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// sourceFileIsEmpty reports whether src is empty or parses as a Go
// file with no non-import declarations.
func sourceFileIsEmpty(src string) bool {
	if strings.TrimSpace(src) == "" {
		return true
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return false
	}
	for _, decl := range file.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue
		}
		return false
	}
	return true
}
