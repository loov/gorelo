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

// assembler holds state shared across the target, source, and rename phases.
type assembler struct {
	ix        *mast.Index
	resolved  []*resolvedRelo
	spans     map[*resolvedRelo]*span
	edits     *ed.Plan
	imports   *importSet
	fileMoves []*fileMoveInfo
	opts      *Options
	plan      *Plan
	es        *editSet

	byTarget map[string][]*resolvedRelo
	bySource map[string][]*resolvedRelo

	// fileMovePaths names every source and target path already written by
	// the whole-file-move pass; the per-decl target/source phases skip
	// these to avoid clobbering the wholesale content.
	fileMovePaths map[string]bool

	// targetNewDecls tracks declarations appended during the target phase.
	// When a file is both source and target, the source phase re-appends
	// these after performing removals on the original on-disk content.
	targetNewDecls map[string]string
}

// assemble builds the final FileEdit list (phase 8).
func assemble(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, edits *ed.Plan, imports *importSet, fileMoves []*fileMoveInfo, opts *Options, plan *Plan) {
	a := &assembler{
		ix:             ix,
		resolved:       resolved,
		spans:          spans,
		edits:          edits,
		imports:        imports,
		fileMoves:      fileMoves,
		opts:           opts,
		plan:           plan,
		es:             newEditSet(),
		byTarget:       groupByTarget(resolved),
		bySource:       groupBySource(resolved),
		targetNewDecls: make(map[string]string),
	}

	a.fileMovePaths = a.assembleFileMoves(fileMoves)
	a.assembleTargets()
	a.assembleSources()
	a.assembleRenames()

	// Final pass: remove unused imports from all emitted files.
	for i, fe := range a.es.Edits() {
		if fe.IsDelete {
			continue
		}
		a.es.edits[i].Content = removeUnusedImportsText(fe.Content)
	}
	plan.Edits = a.es.Edits()
}

func (a *assembler) assembleTargets() {
	sortedTargets := sortedKeys(a.byTarget)
	for _, targetPath := range sortedTargets {
		rrs := a.byTarget[targetPath]
		// Drop relos already emitted wholesale by the file-move pass;
		// only leftover relos (e.g., methods synthesised from sibling
		// files that follow a type defined in the moved file) remain.
		if a.fileMovePaths[targetPath] {
			filtered := rrs[:0:0]
			for _, rr := range rrs {
				if rr.FromFileMove != nil {
					continue
				}
				filtered = append(filtered, rr)
			}
			rrs = filtered
		}
		if len(rrs) == 0 {
			continue
		}

		// Sort by source position.
		sort.SliceStable(rrs, func(i, j int) bool {
			pi := a.ix.Fset.Position(rrs[i].DefIdent.Ident.Pos())
			pj := a.ix.Fset.Position(rrs[j].DefIdent.Ident.Pos())
			if pi.Filename != pj.Filename {
				return pi.Filename < pj.Filename
			}
			return pi.Offset < pj.Offset
		})

		// Check if all sources are the same file as target (same-file rename).
		allSameFile := true
		for _, rr := range rrs {
			if rr.File == nil || rr.File.Path != targetPath {
				allSameFile = false
				break
			}
		}
		if allSameFile {
			// Pure rename in the same file — no target file to create.
			continue
		}

		// Extract declarations.
		type extractedItem struct {
			text      string
			keyword   string
			isGrouped bool
		}
		var extracted []extractedItem
		seenDecls := make(map[ast.Decl]bool)
		crossTargetImports := make(map[string]bool)
		crossTargetAliases := make(map[string]string)

		// Get import changes for this target (used for alias edits).
		ic := a.imports.byFile[targetPath]

		for _, rr := range rrs {
			s := a.spans[rr]
			if s == nil {
				continue
			}

			isGroupedSpec := s.IsGrouped
			keyword := s.Keyword

			if !isGroupedSpec {
				if seenDecls[s.Decl] {
					continue
				}
				seenDecls[s.Decl] = true
			}

			src := fileContent(rr.File)
			if src == nil {
				continue
			}

			// Get rename and cross-target qualification edits for this span.
			// Detach/attach decl edits live in renames.byFile and are
			// extracted into the span by the in-span filter below.
			er := computeExtractedEdits(a.ix, rr, s, a.resolved)
			edits := er.edits

			for impPath := range er.imports {
				crossTargetImports[impPath] = true
			}
			for impPath, alias := range er.aliases {
				if existing, ok := crossTargetAliases[impPath]; ok && existing != alias {
					// Two spans disagree on the alias. Keep the first
					// one — this is rare and both qualify the same
					// package, so the result compiles either way.
					continue
				}
				crossTargetAliases[impPath] = alias
			}

			// Apply self-import unqualification.
			targetDir := filepath.Dir(targetPath)
			targetImportPath := guessImportPath(targetDir)
			if targetImportPath != "" {
				edits = append(edits, collectSelfImportEdits(a.ix, rr, s, targetImportPath)...)
			}

			// Apply import alias edits for collision resolution.
			edits = append(edits, computeImportAliasEdits(a.ix, rr, s, ic)...)

			// Apply rename edits that fall inside the span (e.g., detach
			// call-site rewrites whose enclosing decl is itself moving).
			// Source-file processing filters these out for the source path,
			// so applying them here is the only place they take effect.
			edits = append(edits, planEditsInSpan(a.edits, rr.File.Path, s.Start, s.End)...)

			var text string
			if len(edits) > 0 {
				text = applyEdits(src[s.Start:s.End], edits)
			} else {
				text = string(src[s.Start:s.End])
			}

			if isGroupedSpec {
				text = dedentBlock(text)
			}
			text = strings.TrimRight(text, "\n")

			extracted = append(extracted, extractedItem{
				text:      text,
				keyword:   keyword,
				isGrouped: isGroupedSpec,
			})
		}

		// Add cross-target imports to the import change set.
		if len(crossTargetImports) > 0 {
			if ic == nil {
				ic = a.imports.ensureFile(targetPath)
			}
			for impPath := range crossTargetImports {
				already := false
				for _, entry := range ic.Add {
					if entry.Path == impPath {
						already = true
						break
					}
				}
				if !already {
					entry := importEntry{Path: impPath}
					if alias, ok := crossTargetAliases[impPath]; ok {
						entry.Alias = alias
					}
					ic.Add = append(ic.Add, entry)
				}
			}
		}

		if len(extracted) == 0 {
			continue
		}

		// Render extracted items, grouping consecutive specs.
		var declsBuf strings.Builder
		for i := 0; i < len(extracted); i++ {
			e := extracted[i]
			if !e.isGrouped {
				declsBuf.WriteString("\n")
				declsBuf.WriteString(e.text)
				declsBuf.WriteString("\n")
				continue
			}
			// Collect consecutive grouped specs with same keyword.
			j := i + 1
			for j < len(extracted) && extracted[j].isGrouped && extracted[j].keyword == e.keyword {
				j++
			}
			count := j - i
			if count == 1 {
				declsBuf.WriteString("\n")
				declsBuf.WriteString(prependKeyword(e.text, e.keyword))
				declsBuf.WriteString("\n")
			} else {
				declsBuf.WriteString("\n")
				declsBuf.WriteString(e.keyword + " (\n")
				for k := i; k < j; k++ {
					for line := range strings.SplitSeq(extracted[k].text, "\n") {
						declsBuf.WriteString("\t")
						declsBuf.WriteString(line)
						declsBuf.WriteString("\n")
					}
				}
				declsBuf.WriteString(")\n")
			}
			i = j - 1
		}
		newDecls := declsBuf.String()

		// Check if target file already exists, either in the edit set
		// (e.g., emitted by the file-move pass) or on disk.
		var existing []byte
		var err error
		if content, ok := a.es.Get(targetPath); ok {
			existing = []byte(content)
		} else {
			existing, err = os.ReadFile(targetPath)
		}
		if err == nil && len(existing) > 0 {
			// Append to existing file.
			content := string(existing)

			// Apply rename edits for references in the existing content.
			if targetRenames := planEditsForFile(a.edits, targetPath); len(targetRenames) > 0 {
				content = applyEditsToString(content, targetRenames)
			}

			if ic != nil {
				for _, entry := range ic.Add {
					var warn Warning
					content, warn = ensureImport(content, entry)
					if warn.Message != "" {
						a.plan.Warnings.Add(warn)
					}
				}
			}
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			a.targetNewDecls[targetPath] = newDecls
			content += newDecls
			a.es.Set(FileEdit{Path: targetPath, Content: content})
		} else {
			// New file.
			targetPkgName := determineTargetPkgName(a.ix, rrs)

			// Collect build constraint.
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

			if ic != nil && len(ic.Add) > 0 {
				sortedImports := make([]importEntry, len(ic.Add))
				copy(sortedImports, ic.Add)
				sort.Slice(sortedImports, func(i, j int) bool {
					return sortedImports[i].Path < sortedImports[j].Path
				})
				b.WriteString("\nimport (\n")
				for _, entry := range sortedImports {
					b.WriteString("\t")
					if entry.Alias != "" {
						b.WriteString(entry.Alias)
						b.WriteString(" ")
					}
					b.WriteString(strconv.Quote(entry.Path))
					b.WriteString("\n")
				}
				b.WriteString(")\n")
			}

			b.WriteString(newDecls)
			a.es.Set(FileEdit{Path: targetPath, IsNew: true, Content: b.String()})
		}
	}

}

func (a *assembler) assembleSources() {
	sortedSources := sortedKeys(a.bySource)
	for _, sourcePath := range sortedSources {
		if a.fileMovePaths[sourcePath] {
			continue
		}
		rrs := a.bySource[sourcePath]
		if len(rrs) == 0 {
			continue
		}

		// Check if all relos for this file are same-file a.renames.
		allSameFile := true
		for _, rr := range rrs {
			if rr.TargetFile != sourcePath {
				allSameFile = false
				break
			}
		}

		// If a target edit was already emitted for this path, use its
		// content as the base instead of re-reading from disk.
		var src []byte
		alsoTarget := a.es.Has(sourcePath)
		if content, ok := a.es.Get(sourcePath); ok {
			src = []byte(content)
		} else {
			src = fileContent(rrs[0].File)
		}
		if src == nil {
			continue
		}

		if allSameFile {
			// Same-file renames: apply rename edits only.
			edits := planEditsForFile(a.edits, sourcePath)
			if len(edits) == 0 {
				continue
			}
			newSrc := applyEdits(src, edits)
			a.es.Set(FileEdit{Path: sourcePath, Content: newSrc})
			continue
		}

		// When we are working on the already-emitted target content (which
		// had new declarations appended), we must not remove those newly
		// appended declarations.  The spans were computed against the
		// original on-disk content, so they remain valid only if we apply
		// them to the original bytes.  For the source-also-target case we
		// therefore still use the original on-disk source for removal and
		// then re-append the new declarations that were added by the target
		// phase.
		newDeclSuffix := a.targetNewDecls[sourcePath]
		if alsoTarget {
			origSrc := fileContent(rrs[0].File)
			if origSrc != nil {
				src = origSrc
			}
		}

		// Collect byte ranges to remove.
		type byteRange struct{ start, end int }
		seen := make(map[[2]int]bool)
		var ranges []byteRange
		for _, rr := range rrs {
			if !rr.isCrossFileMove() {
				continue // not being moved, just renamed
			}
			s := a.spans[rr]
			if s == nil {
				continue
			}

			start, end := s.Start, s.End
			key := [2]int{start, end}
			if !seen[key] {
				seen[key] = true
				ranges = append(ranges, byteRange{start, end})
			}
		}

		sort.Slice(ranges, func(i, j int) bool {
			return ranges[i].start < ranges[j].start
		})

		// Merge overlapping ranges.
		merged := ranges[:0:0]
		for _, r := range ranges {
			if len(merged) > 0 && r.start <= merged[len(merged)-1].end {
				if r.end > merged[len(merged)-1].end {
					merged[len(merged)-1].end = r.end
				}
			} else {
				merged = append(merged, r)
			}
		}

		// Get rename edits for remaining code.
		renameEdits := planEditsForFile(a.edits, sourcePath)

		// Filter rename edits that fall inside removed ranges.
		if len(renameEdits) > 0 {
			var filtered []edit
			for _, e := range renameEdits {
				inRemoved := false
				for _, r := range merged {
					if e.Start >= r.start && e.End <= r.end {
						inRemoved = true
						break
					}
				}
				if !inRemoved {
					filtered = append(filtered, e)
				}
			}
			renameEdits = filtered
		}

		// Build edits: removals + a.renames.
		var allEdits []edit
		for _, r := range merged {
			allEdits = append(allEdits, edit{Start: r.start, End: r.end, New: ""})
		}
		allEdits = append(allEdits, renameEdits...)

		newSrc := applyEdits(src, allEdits)

		// Re-append declarations that were added by the target phase.
		if newDeclSuffix != "" {
			newSrc += newDeclSuffix
		}

		// Generate backward-compatibility stubs for cross-package moves.
		if a.opts.stubsEnabled() {
			// Group cross-package relos by target directory so we generate
			// separate stub blocks (and imports) for each target package.
			crossByDir := make(map[string][]*resolvedRelo)
			for _, rr := range rrs {
				if rr.TargetFile == sourcePath || rr.File == nil {
					continue
				}
				// Detach/attach changes the declaration kind (method↔func),
				// which makes backward-compatible stubs impossible.
				if rr.Relo.Detach || rr.Relo.MethodOf != "" {
					continue
				}
				targetDir := finalDir(rr)
				srcDir := filepath.Dir(rr.File.Path)
				if targetDir != srcDir {
					crossByDir[targetDir] = append(crossByDir[targetDir], rr)
				}
			}
			sortedDirs := sortedKeys(crossByDir)
			for _, tDir := range sortedDirs {
				group := crossByDir[tDir]
				targetPkgName := guessPackageName(tDir)
				ar := generateAliases(group, targetPkgName, a.ix.Fset)
				a.plan.Warnings.Add(ar.Warnings...)
				if len(ar.Stubs) > 0 {
					newSrc += "\n" + strings.Join(ar.Stubs, "\n\n") + "\n"
					// Add the import for the target package.
					targetImportPath := guessImportPath(tDir)
					if targetImportPath != "" {
						entry := importEntry{Path: targetImportPath}
						if ar.ImportAlias != "" {
							entry.Alias = ar.ImportAlias
						}
						newSrc, _ = ensureImport(newSrc, entry)
					}
				}
			}
		}

		// Add imports needed by consumer edits (e.g., source-file references
		// to declarations that moved to a different package).
		if ic := a.imports.byFile[sourcePath]; ic != nil {
			newSrc = applyImportEntries(newSrc, ic.Add)
		}

		// Clean up.
		newSrc = removeEmptyDeclBlocks(newSrc)
		newSrc = cleanBlankLines(newSrc)

		if sourceFileIsEmpty(newSrc) {
			a.es.Set(FileEdit{Path: sourcePath, IsDelete: true})
		} else {
			a.es.Set(FileEdit{Path: sourcePath, Content: newSrc})
		}
	}

}

func (a *assembler) assembleRenames() {
	sortedRenameFiles := planEditPaths(a.edits)
	for _, filePath := range sortedRenameFiles {
		edits := planEditsForFile(a.edits, filePath)
		if _, isSource := a.bySource[filePath]; isSource {
			continue
		}
		if a.fileMovePaths[filePath] {
			continue
		}
		if len(edits) == 0 {
			continue
		}

		// If this file was already emitted as a target, renames were
		// already applied during the target phase. Only apply consumer
		// import entries here to avoid double-applying rename edits
		// with stale offsets.
		if content, ok := a.es.Get(filePath); ok {
			if ic, ok := a.imports.byFile[filePath]; ok {
				a.es.Set(FileEdit{Path: filePath, Content: applyImportEntries(content, ic.Add)})
			}
			continue
		}

		src, err := os.ReadFile(filePath)
		if err != nil {
			a.plan.Warnings.Addf("cannot read %s for rename edits: %v", filePath, err)
			continue
		}

		newSrc := applyEdits(src, edits)

		// Apply import additions (e.g., from consumer rewriting).
		if ic, ok := a.imports.byFile[filePath]; ok {
			newSrc = applyImportEntries(newSrc, ic.Add)
		}

		a.es.Set(FileEdit{Path: filePath, Content: newSrc})
	}
}

// computeImportAliasEdits generates edits for an extracted span to rename
// import qualifier idents that were aliased due to collision resolution.
// For each import in rr's source file that received an alias in the target,
// it maps the old local name to the new alias and rewrites SelectorExpr
// qualifiers within the span.
func computeImportAliasEdits(ix *mast.Index, rr *resolvedRelo, s *span, ic *importChange) []edit {
	if rr.File == nil || ic == nil || len(ic.Aliases) == 0 {
		return nil
	}

	// Build a map of oldLocalName -> newAlias for imports used by this rr's source file.
	localToAlias := make(map[string]string)
	for _, imp := range rr.File.Syntax.Imports {
		impPath := importPath(imp)
		alias, ok := ic.Aliases[impPath]
		if !ok {
			continue
		}
		oldLocal := importLocalName(imp, impPath)
		if oldLocal != alias {
			localToAlias[oldLocal] = alias
		}
	}
	if len(localToAlias) == 0 {
		return nil
	}

	var edits []edit
	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		newAlias, ok := localToAlias[ident.Name]
		if !ok {
			return true
		}
		off := ix.Fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if off >= s.Start && endOff <= s.End {
			edits = append(edits, edit{
				Start: off - s.Start,
				End:   endOff - s.Start,
				New:   newAlias,
			})
		}
		return true
	})
	return edits
}

// collectSelfImportEdits finds selector expressions like pkg.Foo where pkg is
// the target package (self-import) and unqualifies them.
func collectSelfImportEdits(ix *mast.Index, rr *resolvedRelo, s *span, selfImportPath string) []edit {
	if rr.File == nil {
		return nil
	}

	selfLocalNames := make(map[string]bool)
	for _, imp := range rr.File.Syntax.Imports {
		impPath := importPath(imp)
		if impPath == selfImportPath {
			if imp.Name != nil {
				selfLocalNames[imp.Name.Name] = true
			} else {
				selfLocalNames[guessImportLocalName(impPath)] = true
			}
		}
	}
	if len(selfLocalNames) == 0 {
		return nil
	}

	var edits []edit
	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if !selfLocalNames[ident.Name] {
			return true
		}

		startOff := ix.Fset.Position(ident.Pos()).Offset
		selOff := ix.Fset.Position(sel.Sel.Pos()).Offset
		if startOff >= s.Start && selOff <= s.End {
			edits = append(edits, edit{
				Start: startOff - s.Start,
				End:   selOff - s.Start,
				New:   "",
			})
		}
		return true
	})
	return edits
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

// applyImportEntries adds each import entry to src in sorted order.
func applyImportEntries(src string, entries []importEntry) string {
	sorted := make([]importEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Path < sorted[j].Path
	})
	for _, entry := range sorted {
		src, _ = ensureImport(src, entry)
	}
	return src
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
