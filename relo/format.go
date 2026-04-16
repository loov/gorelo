package relo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// ensureImport adds an import to the source if not already present.
// Returns the updated source and a warning if the import exists with a
// different alias.
func ensureImport(src string, entry importEntry) (string, Warning) {
	quotedPath := strconv.Quote(entry.Path)
	if existingAlias, has := sourceImportAlias(src, quotedPath); has {
		expectedAlias := entry.Alias
		if expectedAlias == "" {
			expectedAlias = guessImportLocalName(entry.Path)
		}
		existingEffective := existingAlias
		if existingEffective == "" {
			existingEffective = guessImportLocalName(entry.Path)
		}
		if existingEffective != expectedAlias {
			return src, Warnf(
				"import %s exists with alias %q but moved code expects %q",
				quotedPath, existingEffective, expectedAlias)
		}
		return src, Warning{}
	}

	importLine := "\t"
	if entry.Alias != "" {
		importLine += entry.Alias + " "
	}
	importLine += quotedPath

	lines := strings.Split(src, "\n")

	// Look for grouped import block — insert before closing ")".
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "import (" {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == ")" {
					newLines := make([]string, 0, len(lines)+1)
					newLines = append(newLines, lines[:j]...)
					newLines = append(newLines, importLine)
					newLines = append(newLines, lines[j:]...)
					return strings.Join(newLines, "\n"), Warning{}
				}
			}
			// No closing ")" found; insert after "import (" as fallback.
			newLines := make([]string, 0, len(lines)+1)
			newLines = append(newLines, lines[:i+1]...)
			newLines = append(newLines, importLine)
			newLines = append(newLines, lines[i+1:]...)
			return strings.Join(newLines, "\n"), Warning{}
		}
	}

	// Look for single-line import.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") && !strings.HasPrefix(trimmed, "import (") {
			existingImport := "\t" + strings.TrimPrefix(trimmed, "import ")
			newLines := make([]string, 0, len(lines)+3)
			newLines = append(newLines, lines[:i]...)
			newLines = append(newLines, "import (")
			newLines = append(newLines, existingImport)
			newLines = append(newLines, importLine)
			newLines = append(newLines, ")")
			newLines = append(newLines, lines[i+1:]...)
			return strings.Join(newLines, "\n"), Warning{}
		}
	}

	// No import — add after package clause.
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "package ") {
			newLines := make([]string, 0, len(lines)+4)
			newLines = append(newLines, lines[:i+1]...)
			newLines = append(newLines, "")
			newLines = append(newLines, "import (")
			newLines = append(newLines, importLine)
			newLines = append(newLines, ")")
			newLines = append(newLines, lines[i+1:]...)
			return strings.Join(newLines, "\n"), Warning{}
		}
	}

	return src, Warning{}
}

// sourceImportAlias checks if the source already imports the given path
// and returns the alias (or "" if no explicit alias) and whether it was
// found.
func sourceImportAlias(src, quotedPath string) (alias string, found bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return "", false
	}
	for _, imp := range file.Imports {
		if imp.Path.Value == quotedPath {
			if imp.Name != nil {
				return imp.Name.Name, true
			}
			return "", true
		}
	}
	return "", false
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

// sourceFileIsEmpty reports whether src parses as a Go file with no
// non-import declarations.
func sourceFileIsEmpty(src string) bool {
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
