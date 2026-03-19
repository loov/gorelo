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

	"github.com/loov/gorelo/mast"
)

// assemble builds the final FileEdit list (phase 8).
func assemble(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, renames *renameSet, imports *importSet, opts *Options, plan *Plan) {
	// Group relos by target file.
	byTarget := make(map[string][]*resolvedRelo)
	for _, rr := range resolved {
		byTarget[rr.TargetFile] = append(byTarget[rr.TargetFile], rr)
	}

	// Group relos by source file.
	bySource := make(map[string][]*resolvedRelo)
	for _, rr := range resolved {
		if rr.File != nil {
			bySource[rr.File.Path] = append(bySource[rr.File.Path], rr)
		}
	}

	// Sort target paths for deterministic output.
	sortedTargets := sortedKeys(byTarget)

	// Track new declarations appended during the target phase, keyed by
	// target path.  When a file is both source and target, the source phase
	// needs to re-append these after performing removals on the original
	// on-disk content.
	targetNewDecls := make(map[string]string)

	// Build target files.
	for _, targetPath := range sortedTargets {
		rrs := byTarget[targetPath]
		if len(rrs) == 0 {
			continue
		}

		// Sort by source position.
		sort.SliceStable(rrs, func(i, j int) bool {
			pi := ix.Fset.Position(rrs[i].DefIdent.Ident.Pos())
			pj := ix.Fset.Position(rrs[j].DefIdent.Ident.Pos())
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

		// Get import changes for this target (used for alias edits).
		ic := imports.byFile[targetPath]

		for _, rr := range rrs {
			s := spans[rr]
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

			// Get rename edits for this span.
			edits := computeExtractedRenames(ix, rr, s, resolved)

			// Apply self-import unqualification.
			targetDir := filepath.Dir(targetPath)
			targetImportPath := guessImportPath(targetDir)
			if targetImportPath != "" {
				edits = append(edits, collectSelfImportEdits(ix, rr, s, targetImportPath)...)
			}

			// Apply import alias edits for collision resolution.
			edits = append(edits, computeImportAliasEdits(ix, rr, s, ic)...)

			// Apply cross-target reference qualification.
			crossEdits, crossImps := computeCrossTargetEdits(ix, rr, s, resolved)
			edits = append(edits, crossEdits...)
			for impPath := range crossImps {
				crossTargetImports[impPath] = true
			}

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
				ic = imports.ensureFile(targetPath)
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
					ic.Add = append(ic.Add, importEntry{Path: impPath})
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
					for _, line := range strings.Split(extracted[k].text, "\n") {
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

		// Check if target file already exists.
		existing, err := os.ReadFile(targetPath)
		if err == nil && len(existing) > 0 {
			// Append to existing file.
			content := string(existing)

			// Apply rename edits for references in the existing content.
			if targetRenames := renames.byFile[targetPath]; len(targetRenames) > 0 {
				content = applyEditsToString(content, targetRenames)
			}

			if ic != nil {
				for _, entry := range ic.Add {
					var warn Warning
					content, warn = ensureImport(content, entry)
					if warn.Message != "" {
						plan.Warnings.Add(warn)
					}
				}
			}
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			targetNewDecls[targetPath] = newDecls
			content += newDecls
			content = removeUnusedImportsText(content)
			plan.Edits = append(plan.Edits, FileEdit{
				Path:    targetPath,
				Content: content,
			})
		} else {
			// New file.
			targetPkgName := determineTargetPkgName(rrs)

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
			content := removeUnusedImportsText(b.String())
			plan.Edits = append(plan.Edits, FileEdit{
				Path:    targetPath,
				IsNew:   true,
				Content: content,
			})
		}
	}

	// Process source files: remove moved declarations.
	// Build an index of target edits already emitted, so when a file is both
	// a source and a target we update the existing edit rather than creating a
	// conflicting duplicate.
	targetEditIdx := make(map[string]int) // path -> index in plan.Edits
	for i, fe := range plan.Edits {
		targetEditIdx[fe.Path] = i
	}

	sortedSources := sortedKeys(bySource)
	for _, sourcePath := range sortedSources {
		rrs := bySource[sourcePath]
		if len(rrs) == 0 {
			continue
		}

		// Check if all relos for this file are same-file renames.
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
		existingIdx := -1
		if idx, ok := targetEditIdx[sourcePath]; ok {
			src = []byte(plan.Edits[idx].Content)
			existingIdx = idx
		} else {
			src = fileContent(rrs[0].File)
		}
		if src == nil {
			continue
		}

		if allSameFile {
			// Same-file renames: apply rename edits only.
			edits := renames.byFile[sourcePath]
			if len(edits) == 0 {
				continue
			}
			newSrc := applyEdits(src, edits)
			if existingIdx >= 0 {
				plan.Edits[existingIdx].Content = newSrc
			} else {
				plan.Edits = append(plan.Edits, FileEdit{
					Path:    sourcePath,
					Content: newSrc,
				})
			}
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
		newDeclSuffix := targetNewDecls[sourcePath]
		if existingIdx >= 0 {
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
			if rr.TargetFile == sourcePath {
				continue // not being moved, just renamed
			}
			s := spans[rr]
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
		renameEdits := renames.byFile[sourcePath]

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

		// Build edits: removals + renames.
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
		if opts != nil && opts.Stubs {
			// Group cross-package relos by target directory so we generate
			// separate stub blocks (and imports) for each target package.
			crossByDir := make(map[string][]*resolvedRelo)
			for _, rr := range rrs {
				if rr.TargetFile == sourcePath || rr.File == nil {
					continue
				}
				targetDir := filepath.Dir(rr.TargetFile)
				srcDir := filepath.Dir(rr.File.Path)
				if targetDir != srcDir {
					crossByDir[targetDir] = append(crossByDir[targetDir], rr)
				}
			}
			sortedDirs := sortedKeys(crossByDir)
			for _, tDir := range sortedDirs {
				group := crossByDir[tDir]
				targetPkgName := guessPackageName(tDir)
				ar := generateAliases(group, targetPkgName, ix.Fset)
				plan.Warnings.Add(ar.Warnings...)
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
		if ic := imports.byFile[sourcePath]; ic != nil {
			sortedAdd := make([]importEntry, len(ic.Add))
			copy(sortedAdd, ic.Add)
			sort.Slice(sortedAdd, func(i, j int) bool {
				return sortedAdd[i].Path < sortedAdd[j].Path
			})
			for _, entry := range sortedAdd {
				newSrc, _ = ensureImport(newSrc, entry)
			}
		}

		// Clean up.
		newSrc = removeEmptyDeclBlocks(newSrc)
		newSrc = cleanBlankLines(newSrc)
		newSrc = removeUnusedImportsText(newSrc)

		if sourceFileIsEmpty(newSrc) {
			if existingIdx >= 0 {
				plan.Edits[existingIdx] = FileEdit{
					Path:     sourcePath,
					IsDelete: true,
				}
			} else {
				plan.Edits = append(plan.Edits, FileEdit{
					Path:     sourcePath,
					IsDelete: true,
				})
			}
		} else {
			if existingIdx >= 0 {
				plan.Edits[existingIdx] = FileEdit{
					Path:    sourcePath,
					Content: newSrc,
				}
			} else {
				plan.Edits = append(plan.Edits, FileEdit{
					Path:    sourcePath,
					Content: newSrc,
				})
			}
		}
	}

	// Collect already-emitted paths so we can detect conflicts.
	emittedPaths := make(map[string]int) // path -> index in plan.Edits
	for i, fe := range plan.Edits {
		emittedPaths[fe.Path] = i
	}

	// Apply renames to other files not involved as source/target.
	sortedRenameFiles := sortedKeys(renames.byFile)
	for _, filePath := range sortedRenameFiles {
		edits := renames.byFile[filePath]
		if _, isSource := bySource[filePath]; isSource {
			continue
		}
		if len(edits) == 0 {
			continue
		}

		// If this file was already emitted as a target, apply renames
		// to the already-computed content instead of reading from disk.
		if idx, already := emittedPaths[filePath]; already {
			existing := plan.Edits[idx]
			if !existing.IsDelete {
				newSrc := applyEditsToString(existing.Content, edits)
				if ic, ok := imports.byFile[filePath]; ok {
					sortedAdd := make([]importEntry, len(ic.Add))
					copy(sortedAdd, ic.Add)
					sort.Slice(sortedAdd, func(i, j int) bool {
						return sortedAdd[i].Path < sortedAdd[j].Path
					})
					for _, entry := range sortedAdd {
						newSrc, _ = ensureImport(newSrc, entry)
					}
				}
				newSrc = removeUnusedImportsText(newSrc)
				plan.Edits[idx].Content = newSrc
			}
			continue
		}

		src, err := os.ReadFile(filePath)
		if err != nil {
			plan.Warnings.Addf("cannot read %s for rename edits: %v", filePath, err)
			continue
		}

		newSrc := applyEdits(src, edits)

		// Apply import additions (e.g., from consumer rewriting).
		if ic, ok := imports.byFile[filePath]; ok {
			sortedAdd := make([]importEntry, len(ic.Add))
			copy(sortedAdd, ic.Add)
			sort.Slice(sortedAdd, func(i, j int) bool {
				return sortedAdd[i].Path < sortedAdd[j].Path
			})
			for _, entry := range sortedAdd {
				newSrc, _ = ensureImport(newSrc, entry)
			}
		}

		newSrc = removeUnusedImportsText(newSrc)
		plan.Edits = append(plan.Edits, FileEdit{
			Path:    filePath,
			Content: newSrc,
		})
	}
}

// computeCrossTargetEdits generates edits for an extracted span to qualify
// references to declarations being moved to a DIFFERENT target package.
// It also returns the set of import paths that need to be added for those
// cross-target references.
func computeCrossTargetEdits(ix *mast.Index, rr *resolvedRelo, s *span, resolved []*resolvedRelo) ([]edit, map[string]bool) {
	if rr.File == nil || s == nil {
		return nil, nil
	}

	targetDir := filepath.Dir(rr.TargetFile)

	// Build a map of groups being moved to other target directories.
	type crossInfo struct {
		tgtPkgPath string
		tgtName    string
	}
	crossGroups := make(map[*mast.Group]*crossInfo)
	for _, r := range resolved {
		if r.File == nil {
			continue
		}
		rDir := filepath.Dir(r.TargetFile)
		if rDir == targetDir {
			continue // same target directory, no qualification needed
		}
		srcDir := filepath.Dir(r.File.Path)
		if srcDir == targetDir {
			continue // declaration is already in our target package
		}
		tgtPkgPath := guessImportPath(rDir)
		if tgtPkgPath == "" {
			continue
		}
		crossGroups[r.Group] = &crossInfo{
			tgtPkgPath: tgtPkgPath,
			tgtName:    r.TargetName,
		}
	}
	if len(crossGroups) == 0 {
		return nil, nil
	}

	var edits []edit
	neededImports := make(map[string]bool)

	// Walk AST within the span to find idents referencing cross-target groups.
	ast.Inspect(rr.File.Syntax, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		grp := ix.Group(ident)
		if grp == nil {
			return true
		}
		info, ok := crossGroups[grp]
		if !ok {
			return true
		}

		off := ix.Fset.Position(ident.Pos()).Offset
		endOff := off + len(ident.Name)
		if off >= s.Start && endOff <= s.End {
			tgtLocalName := guessImportLocalName(info.tgtPkgPath)
			edits = append(edits, edit{
				Start: off - s.Start,
				End:   endOff - s.Start,
				New:   tgtLocalName + "." + info.tgtName,
			})
			neededImports[info.tgtPkgPath] = true
		}
		return true
	})

	return deduplicateEdits(edits), neededImports
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
		impPath, _ := strconv.Unquote(imp.Path.Value)
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
		impPath, _ := strconv.Unquote(imp.Path.Value)
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
func determineTargetPkgName(rrs []*resolvedRelo) string {
	for _, rr := range rrs {
		if rr.File != nil {
			srcDir := filepath.Dir(rr.File.Path)
			targetDir := filepath.Dir(rr.TargetFile)
			if srcDir == targetDir {
				return rr.File.Syntax.Name.Name
			}
		}
	}
	// Guess from directory name.
	if len(rrs) > 0 {
		return guessPackageName(filepath.Dir(rrs[0].TargetFile))
	}
	return "pkg"
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
		impPath, _ := strconv.Unquote(imp.Path.Value)
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
		return "pkg"
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
