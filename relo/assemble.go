package relo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// assembler holds the state shared by the file-move, plan.Apply, and
// import-application passes that compose Plan.Edits.
type assembler struct {
	ix       *mast.Index
	resolved []*resolvedRelo
	spans    map[*resolvedRelo]*span
	edits    *ed.Plan
	imports  *importSet
	opts     *Options
	plan     *Plan
	es       *editSet

	byTarget map[string][]*resolvedRelo
	bySource map[string][]*resolvedRelo

	// fileMovePaths names every source and target path written by the
	// whole-file-move pass; singlePassApply special-cases these so the
	// pre-rendered file-move content is preserved.
	fileMovePaths map[string]bool
}

// assemble builds the final FileEdit list (phase 8).
func assemble(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, edits *ed.Plan, imports *importSet, fileMoves []*fileMoveInfo, opts *Options, plan *Plan) {
	a := &assembler{
		ix:       ix,
		resolved: resolved,
		spans:    spans,
		edits:    edits,
		imports:  imports,
		opts:     opts,
		plan:     plan,
		es:       newEditSet(),
		byTarget: groupByTarget(resolved),
		bySource: groupBySource(resolved),
	}

	a.fileMovePaths = a.assembleFileMoves(fileMoves)
	a.singlePassApply()
	a.applyImportsPass()

	// Final pass: remove unused imports from all emitted files.
	for i, fe := range a.es.Edits() {
		if fe.IsDelete {
			continue
		}
		a.es.edits[i].Content = removeUnusedImportsText(fe.Content)
	}
	plan.Edits = a.es.Edits()
}

// applyImportsPass adds importSet entries to each affected file's
// post-Apply content. It runs after the main assembly so it operates
// on the final layout produced by plan.Apply, and before
// removeUnusedImportsText so any imports it adds that turn out to be
// unused get pruned in the same run. Entries are pre-deduped and
// alias-resolved at addition time (see addImportEntry); ensureImport
// handles the actual insertion.
func (a *assembler) applyImportsPass() {
	for _, filePath := range sortedKeys(a.imports.byFile) {
		ic := a.imports.byFile[filePath]
		if ic == nil || len(ic.Add) == 0 {
			continue
		}
		content, ok := a.es.Get(filePath)
		if !ok {
			continue
		}
		sorted := make([]importEntry, len(ic.Add))
		copy(sorted, ic.Add)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Path < sorted[j].Path
		})
		for _, entry := range sorted {
			var warn Warning
			content, warn = ensureImport(content, entry)
			if warn.Message != "" {
				a.plan.Warnings.Add(warn)
			}
		}
		a.es.Set(FileEdit{Path: filePath, Content: content})
	}
}

// singlePassApply gathers the input contents for every file the main
// pass will touch (sources, existing-and-new targets, consumers) and
// runs plan.Apply once. The Plan already contains every relevant
// primitive: rename Replaces, detach/attach call-site rewrites, and
// the Move + carried qualification primitives emitted by
// emitCrossFileExtraction. plan.Apply atomically deletes moved spans
// in source files and appends rendered content (via each Move's
// GroupRender) to the corresponding targets. New target files are
// pre-seeded with the package preamble so Move at offset -1 lands
// after it.
//
// Per-file post-processing (stub appending, removeEmptyDeclBlocks,
// cleanBlankLines, sourceFileIsEmpty → IsDelete) runs over each
// output. File-move-handled paths are skipped.
func (a *assembler) singlePassApply() {
	// inputPaths is every file plan.Apply must see: source files,
	// target files (existing or new), consumer files via planEditPaths,
	// plus file-move source/target paths so the primitives targeting
	// them (detach/attach decl edits, per-decl Moves into a file-moved
	// target) are in-bounds. The output for file-move source paths is
	// discarded since assembleFileMoves owns the IsDelete state; the
	// output for file-move target paths replaces the editSet entry so
	// per-decl Moves' additions take effect.
	inputPaths := make(map[string]bool)
	for path := range a.bySource {
		inputPaths[path] = true
	}
	for path := range a.byTarget {
		inputPaths[path] = true
	}
	for _, path := range planEditPaths(a.edits) {
		inputPaths[path] = true
	}

	inputs := make(map[string][]byte, len(inputPaths))
	existedBefore := make(map[string]bool, len(inputPaths))
	for path := range inputPaths {
		// File-move targets: pre-load the assembled content so per-decl
		// Moves at offset -1 append to it.
		if a.fileMovePaths[path] {
			if content, ok := a.es.Get(path); ok {
				inputs[path] = []byte(content)
				existedBefore[path] = true
				continue
			}
			// File-move source whose entry is IsDelete: still feed the
			// original bytes in so primitives don't run out of bounds.
			if f := a.ix.FilesByPath[path]; f != nil {
				dup := make([]byte, len(f.Src))
				copy(dup, f.Src)
				inputs[path] = dup
			}
			continue
		}
		if f := a.ix.FilesByPath[path]; f != nil {
			dup := make([]byte, len(f.Src))
			copy(dup, f.Src)
			inputs[path] = dup
			existedBefore[path] = true
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			inputs[path] = data
			existedBefore[path] = true
			continue
		}
		// New target file — pre-seed with preamble so Move at offset -1
		// lands after it.
		if rrs, ok := a.byTarget[path]; ok {
			inputs[path] = []byte(buildTargetPreamble(a.ix, rrs))
		}
	}

	outputs, err := a.edits.Apply(inputs)
	if err != nil {
		a.plan.Warnings.Addf("plan.Apply failed: %v", err)
		return
	}

	stubs := a.generateAllStubs()

	sortedPaths := make([]string, 0, len(inputPaths))
	for p := range inputPaths {
		sortedPaths = append(sortedPaths, p)
	}
	sort.Strings(sortedPaths)
	for _, path := range sortedPaths {
		// File-move sources whose editSet entry is IsDelete keep that
		// state; discard plan.Apply's modified output.
		if a.fileMovePaths[path] {
			if existing, ok := a.es.Get(path); !ok || existing == "" {
				continue
			}
		}
		out, ok := outputs[path]
		if !ok {
			continue
		}
		text := string(out)
		if stub, has := stubs[path]; has {
			text += stub
		}
		text = removeEmptyDeclBlocks(text)
		text = cleanBlankLines(text)
		if _, isSource := a.bySource[path]; isSource && !a.fileMovePaths[path] {
			if sourceFileIsEmpty(text) {
				a.es.Set(FileEdit{Path: path, IsDelete: true})
				continue
			}
		}
		a.es.Set(FileEdit{Path: path, Content: text, IsNew: !existedBefore[path]})
	}
}

// buildTargetPreamble returns the package preamble that pre-seeds a
// new target file's input to plan.Apply: an optional `//go:build`
// constraint (when all contributing rrs share one), then the package
// clause. Move primitives at offset -1 land after this preamble.
func buildTargetPreamble(ix *mast.Index, rrs []*resolvedRelo) string {
	targetPkgName := determineTargetPkgName(ix, rrs)
	constraint := collectBuildConstraint(rrs)
	var b strings.Builder
	if constraint != "" {
		b.WriteString("//go:build ")
		b.WriteString(constraint)
		b.WriteString("\n\n")
	}
	b.WriteString("package ")
	b.WriteString(targetPkgName)
	b.WriteString("\n")
	return b.String()
}

// generateAllStubs produces backward-compatibility stub text per source
// file (one block per cross-package target directory) and registers
// the matching imports on the importSet. Returns the per-file text to
// append to plan.Apply's source output. Empty when @stubs is off or
// no cross-package moves apply.
func (a *assembler) generateAllStubs() map[string]string {
	out := make(map[string]string)
	if !a.opts.stubsEnabled() {
		return out
	}
	sortedSources := sortedKeys(a.bySource)
	for _, sourcePath := range sortedSources {
		if a.fileMovePaths[sourcePath] {
			continue
		}
		rrs := a.bySource[sourcePath]
		crossByDir := make(map[string][]*resolvedRelo)
		for _, rr := range rrs {
			if rr.TargetFile == sourcePath || rr.File == nil {
				continue
			}
			if rr.Relo.Detach || rr.Relo.MethodOf != "" {
				continue
			}
			targetDir := finalDir(rr)
			srcDir := filepath.Dir(rr.File.Path)
			if targetDir != srcDir {
				crossByDir[targetDir] = append(crossByDir[targetDir], rr)
			}
		}
		var b strings.Builder
		for _, tDir := range sortedKeys(crossByDir) {
			group := crossByDir[tDir]
			targetPkgName := guessPackageName(tDir)
			ar := generateAliases(group, targetPkgName, a.ix.Fset)
			a.plan.Warnings.Add(ar.Warnings...)
			if len(ar.Stubs) == 0 {
				continue
			}
			b.WriteString("\n")
			b.WriteString(strings.Join(ar.Stubs, "\n\n"))
			b.WriteString("\n")
			if targetImportPath := guessImportPath(tDir); targetImportPath != "" {
				entry := importEntry{Path: targetImportPath}
				if ar.ImportAlias != "" {
					entry.Alias = ar.ImportAlias
				}
				addImportEntry(a.imports, a.ix, sourcePath, entry)
			}
		}
		if b.Len() > 0 {
			out[sourcePath] = b.String()
		}
	}
	return out
}

// determineTargetPkgName figures out the package name for a new target file.
func determineTargetPkgName(ix *mast.Index, rrs []*resolvedRelo) string {
	for _, rr := range rrs {
		if rr.File != nil && rr.File.Pkg != nil {
			if isSamePackageDir(rr.File.Pkg, rr.TargetFile) {
				return rr.File.Syntax.Name.Name
			}
		}
	}
	// Check if there are existing files in the target directory whose
	// package name we should match (handles package name != dir name).
	if len(rrs) > 0 {
		targetDir := filepath.Dir(rrs[0].TargetFile)
		for _, pkg := range ix.Pkgs {
			if len(pkg.Files) == 0 {
				continue
			}
			// Skip external test packages (package foo_test) — they
			// share the directory but shouldn't determine the package
			// name for new production files.
			if strings.HasSuffix(pkg.Name, "_test") {
				continue
			}
			if filepath.Dir(pkg.Files[0].Path) == targetDir {
				return pkg.Name
			}
		}
		return guessPackageName(targetDir)
	}
	return "unable_to_determine_package"
}

// collectBuildConstraint determines constraint for a new target file.
func collectBuildConstraint(rrs []*resolvedRelo) string {
	seen := make(map[string]bool)
	for _, rr := range rrs {
		if rr.File != nil {
			seen[rr.File.BuildTag] = true
		}
	}
	hasUnconstrained := seen[""]
	delete(seen, "")
	if len(seen) == 0 || hasUnconstrained || len(seen) > 1 {
		return ""
	}
	for c := range seen {
		return c
	}
	return ""
}

// ensureImport adds an import to the source if not already present.
// Returns the updated source and a warning if the import exists with a different alias.
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
			// Find the closing ")".
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
// and returns the alias (or "" if no explicit alias) and whether it was found.
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

// removeUnusedImportsText re-parses and removes unused imports.
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

// removeEmptyDeclBlocks removes empty declaration blocks like "import (\n)".
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
					// Skip the entire empty block.
					i = j + 1
					// Also skip surrounding blank lines.
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

// cleanBlankLines collapses runs of more than one blank line.
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

// sourceFileIsEmpty checks if a Go source has no declarations.
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

// guessPackageName derives a package name from a directory path.
func guessPackageName(dir string) string {
	base := filepath.Base(dir)
	base = strings.ReplaceAll(base, "-", "")
	base = strings.ReplaceAll(base, ".", "")
	if base == "" {
		return "unable_to_determine_package"
	}
	return base
}

// sortedKeys returns sorted keys of a map.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fileContent returns the source bytes of a mast.File.
func fileContent(f *mast.File) []byte {
	if f == nil {
		return nil
	}
	return f.Src
}
