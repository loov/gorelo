package relo

import (
	"os"
	"path/filepath"
	"sort"
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

	// out collects per-path FileEdits as the assembly passes run. The
	// final plan.Edits slice is built from out at the end of assemble.
	out map[string]FileEdit

	byTarget map[string][]*resolvedRelo
	bySource map[string][]*resolvedRelo

	// fileMovePaths names every source and target path written by the
	// file-move pass; gatherInputs and postProcess special-case these
	// so the pre-rendered file-move content is preserved.
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
		out:      make(map[string]FileEdit),
		byTarget: groupByTarget(resolved),
		bySource: groupBySource(resolved),
	}

	// Step 1: render file moves and gather inputs for plan.Apply.
	a.fileMovePaths = a.assembleFileMoves(fileMoves)
	inputs, existedBefore := a.gatherInputs()

	// Step 2: apply all edit primitives at once.
	outputs, err := a.edits.Apply(inputs)
	if err != nil {
		a.plan.Warnings.Addf("plan.Apply failed: %v", err)
		return
	}

	// Step 3: post-process each output into FileEdits and materialize
	// plan.Edits in path-sorted order.
	a.postProcess(outputs, existedBefore)
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
		fe, ok := a.out[filePath]
		if !ok || fe.IsDelete {
			continue
		}
		content := fe.Content
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
		fe.Content = content
		a.out[filePath] = fe
	}
}

// gatherInputs collects the byte contents for every file that
// plan.Apply must see: source files, target files (existing or new),
// consumer files, and file-move source/target paths. New target files
// are pre-seeded with the package preamble so Move at offset -1 lands
// after it. existedBefore tracks which paths had content before the
// run (used to set FileEdit.IsNew in post-processing).
func (a *assembler) gatherInputs() (inputs map[string][]byte, existedBefore map[string]bool) {
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

	inputs = make(map[string][]byte, len(inputPaths))
	existedBefore = make(map[string]bool, len(inputPaths))
	for path := range inputPaths {
		// File-move targets: pre-load the rendered content so per-decl
		// Moves at offset -1 append to it.
		if a.fileMovePaths[path] {
			if fe, ok := a.out[path]; ok && !fe.IsDelete {
				inputs[path] = []byte(fe.Content)
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
	return inputs, existedBefore
}

// postProcess converts plan.Apply outputs into FileEdits. For each
// output file it appends stubs, cleans up empty declaration blocks
// and blank-line runs, marks emptied source files for deletion, adds
// queued imports, removes unused imports, and materializes plan.Edits
// in path-sorted order.
func (a *assembler) postProcess(outputs map[string][]byte, existedBefore map[string]bool) {
	stubs := a.generateAllStubs()

	sortedPaths := make([]string, 0, len(outputs))
	for p := range outputs {
		sortedPaths = append(sortedPaths, p)
	}
	sort.Strings(sortedPaths)
	for _, path := range sortedPaths {
		// File-move sources whose entry is IsDelete keep that state;
		// discard plan.Apply's modified output.
		if a.fileMovePaths[path] {
			if fe, ok := a.out[path]; !ok || fe.IsDelete || fe.Content == "" {
				continue
			}
		}
		text := string(outputs[path])
		if stub, has := stubs[path]; has {
			text += stub
		}
		text = removeEmptyDeclBlocks(text)
		text = cleanBlankLines(text)
		if _, isSource := a.bySource[path]; isSource && !a.fileMovePaths[path] {
			if sourceFileIsEmpty(text) {
				a.out[path] = FileEdit{Path: path, IsDelete: true}
				continue
			}
		}
		a.out[path] = FileEdit{Path: path, Content: text, IsNew: !existedBefore[path]}
	}

	a.applyImportsPass()

	for path, fe := range a.out {
		if fe.IsDelete {
			continue
		}
		fe.Content = removeUnusedImportsText(fe.Content)
		a.out[path] = fe
	}
	a.plan.Edits = make([]FileEdit, 0, len(a.out))
	for _, path := range sortedKeys(a.out) {
		a.plan.Edits = append(a.plan.Edits, a.out[path])
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
		if isFileMoveSource(a.bySource[sourcePath]) {
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
