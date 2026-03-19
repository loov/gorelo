package relo

import (
	"strconv"

	"github.com/loov/gorelo/mast"
)

// computeConsumerEdits finds files in the index that reference moved groups
// from external packages and generates edits to update their qualifier
// expressions and imports. Results are merged into the provided renameSet
// and importSet so that the assembly phase applies them uniformly.
func computeConsumerEdits(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, renames *renameSet, imports *importSet, plan *Plan) {
	// Build lookup structures.
	type moveInfo struct {
		srcPkgPath string // source package import path
		tgtPkgPath string // target package import path
		tgtName    string // name at the target (may differ if renamed)
	}

	// Collect cross-package moves keyed by group.
	movedGroups := make(map[*mast.Group]*moveInfo)
	sourceFiles := make(map[string]bool)
	targetFiles := make(map[string]bool)

	for _, rr := range resolved {
		if rr.File != nil {
			sourceFiles[rr.File.Path] = true
		}
		targetFiles[rr.TargetFile] = true

		if rr.File == nil {
			continue
		}
		srcDir := dirOf(rr.File.Path)
		tgtDir := dirOf(rr.TargetFile)
		if srcDir == tgtDir {
			continue // same-package move, no consumer rewriting needed
		}

		srcPkgPath := guessImportPath(srcDir)
		tgtPkgPath := guessImportPath(tgtDir)
		if srcPkgPath == "" || tgtPkgPath == "" {
			continue
		}

		movedGroups[rr.Group] = &moveInfo{
			srcPkgPath: srcPkgPath,
			tgtPkgPath: tgtPkgPath,
			tgtName:    rr.TargetName,
		}
	}

	if len(movedGroups) == 0 {
		return
	}

	// Scan all Use idents in moved groups looking for consumer references.
	// A consumer reference is a Use ident in a file that is neither a source
	// nor a target file.
	type consumerEdit struct {
		qualifierEdit *edit      // edit to change the qualifier (pkg alias)
		nameEdit      *edit      // edit to change the ident name (if renamed)
		tgtPkgPath    string     // import path to add
		srcPkgPath    string     // import path potentially to remove
		file          *mast.File // consumer file
	}

	// Collect edits per consumer file.
	type fileEdits struct {
		qualifierEdits []edit
		nameEdits      []edit
		addImports     map[string]bool // target import paths to add
		srcImports     map[string]bool // source import paths that may become unused
	}
	byFile := make(map[string]*fileEdits)

	ensureFile := func(path string) *fileEdits {
		fe, ok := byFile[path]
		if !ok {
			fe = &fileEdits{
				addImports: make(map[string]bool),
				srcImports: make(map[string]bool),
			}
			byFile[path] = fe
		}
		return fe
	}

	for grp, info := range movedGroups {
		for _, id := range grp.Idents {
			if id.Kind != mast.Use || id.File == nil {
				continue
			}
			filePath := id.File.Path
			if sourceFiles[filePath] || targetFiles[filePath] {
				continue
			}
			// This ident must have a qualifier (pkg.Name) since it's a
			// cross-package reference. Skip if not qualified.
			if id.Qualifier == nil {
				continue
			}

			fe := ensureFile(filePath)

			// Determine what the new qualifier text should be.
			// We'll use the target package's guessed local name.
			tgtLocalName := guessImportLocalName(info.tgtPkgPath)

			// Edit the qualifier ident (e.g., "oldpkg" -> "newpkg").
			qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
			qualEnd := qualOff + len(id.Qualifier.Name)
			if id.Qualifier.Name != tgtLocalName {
				fe.qualifierEdits = append(fe.qualifierEdits, edit{
					Start: qualOff,
					End:   qualEnd,
					New:   tgtLocalName,
				})
			}

			// Edit the ident name if it was renamed.
			if info.tgtName != grp.Name {
				identOff := ix.Fset.Position(id.Ident.Pos()).Offset
				identEnd := identOff + len(id.Ident.Name)
				fe.nameEdits = append(fe.nameEdits, edit{
					Start: identOff,
					End:   identEnd,
					New:   info.tgtName,
				})
			}

			fe.addImports[info.tgtPkgPath] = true
			fe.srcImports[info.srcPkgPath] = true
		}
	}

	// Merge consumer edits into the rename set and import set.
	for filePath, fe := range byFile {
		// Merge qualifier and name edits into renames.
		allEdits := append(fe.qualifierEdits, fe.nameEdits...)
		allEdits = deduplicateEdits(allEdits)
		if len(allEdits) > 0 {
			renames.byFile[filePath] = append(renames.byFile[filePath], allEdits...)
			renames.byFile[filePath] = deduplicateEdits(renames.byFile[filePath])
		}

		// Add target imports.
		for tgtPath := range fe.addImports {
			ic := imports.ensureFile(filePath)

			// Check if the target import already exists in the file.
			f := findFileInIndex(ix, filePath)
			if f != nil {
				alreadyImported := false
				for _, imp := range f.Syntax.Imports {
					impPath, _ := strconv.Unquote(imp.Path.Value)
					if impPath == tgtPath {
						alreadyImported = true
						break
					}
				}
				if alreadyImported {
					continue
				}
			}

			ic.Add = append(ic.Add, importEntry{Path: tgtPath})
		}

		// Mark source imports for potential removal. The actual removal
		// is handled by removeUnusedImportsText in the assembly phase,
		// which checks whether any references remain.
		// We don't explicitly remove here because other symbols from
		// the source package may still be used.
	}
}
