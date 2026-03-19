package relo

import (
	"path/filepath"
	"sort"

	"github.com/loov/gorelo/mast"
)

// computeConsumerEdits finds files in the index that reference moved groups
// from external packages and generates edits to update their qualifier
// expressions and imports. Results are merged into the provided renameSet
// and importSet so that the assembly phase applies them uniformly.
func computeConsumerEdits(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, renames *renameSet, imports *importSet, opts *Options, plan *Plan) {
	// Build lookup structures.
	type moveInfo struct {
		srcPkgPath string // source package import path
		tgtPkgPath string // target package import path
		tgtName    string // name at the target (may differ if renamed)
	}

	// Collect cross-package moves keyed by group.
	movedGroups := make(map[*mast.Group]*moveInfo)

	// Track target directories per group so we can skip consumer edits
	// for files in the same package as the group's destination (where
	// the declaration becomes local). We use per-group target dirs
	// rather than a blanket set so that a file which is a target for
	// one group can still receive consumer edits for a different group.
	groupTargetDirs := make(map[*mast.Group]map[string]bool)

	for _, rr := range resolved {
		dir := filepath.Dir(rr.TargetFile)
		if groupTargetDirs[rr.Group] == nil {
			groupTargetDirs[rr.Group] = make(map[string]bool)
		}
		groupTargetDirs[rr.Group][dir] = true

		if rr.File == nil {
			continue
		}
		srcDir := filepath.Dir(rr.File.Path)
		tgtDir := filepath.Dir(rr.TargetFile)
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

	// Collect edits per consumer file.
	type fileEdits struct {
		qualifierEdits []edit
		nameEdits      []edit
		addImports     map[string]bool // target import paths to add
	}
	byFile := make(map[string]*fileEdits)

	ensureFile := func(path string) *fileEdits {
		fe, ok := byFile[path]
		if !ok {
			fe = &fileEdits{
				addImports: make(map[string]bool),
			}
			byFile[path] = fe
		}
		return fe
	}

	// Build moved span lookup so we can skip idents inside extracted code.
	movedSpans := buildMovedSpanIndex(resolved, spans)

	for grp, info := range movedGroups {
		for _, id := range grp.Idents {
			if id.Kind != mast.Use || id.File == nil {
				continue
			}
			filePath := id.File.Path
			inTargetPkg := groupTargetDirs[grp][filepath.Dir(filePath)]

			// File is in the target package: the declaration is
			// becoming local. Unqualify qualified references (e.g.,
			// src.Greet -> Greet) and apply renames if needed.
			// Unqualified references already work as-is.
			if inTargetPkg {
				if id.Qualifier == nil {
					// Already unqualified; apply rename if needed.
					if info.tgtName != grp.Name {
						identOff := ix.Fset.Position(id.Ident.Pos()).Offset
						identEnd := identOff + len(id.Ident.Name)
						if movedSpans.Contains(filePath, identOff, identEnd) {
							continue
						}
						fe := ensureFile(filePath)
						fe.nameEdits = append(fe.nameEdits, edit{
							Start: identOff,
							End:   identEnd,
							New:   info.tgtName,
						})
					}
					continue
				}
				// Qualified reference (e.g., src.Greet): remove qualifier.
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				selOff := ix.Fset.Position(id.Ident.Pos()).Offset
				fe := ensureFile(filePath)
				fe.qualifierEdits = append(fe.qualifierEdits, edit{
					Start: qualOff,
					End:   selOff, // removes "src."
					New:   "",
				})
				// Apply rename if needed.
				if info.tgtName != grp.Name {
					identEnd := selOff + len(id.Ident.Name)
					fe.nameEdits = append(fe.nameEdits, edit{
						Start: selOff,
						End:   identEnd,
						New:   info.tgtName,
					})
				}
				continue
			}

			// Unqualified same-package reference: the declaration is
			// moving to a different package, so we need to add a
			// package qualifier (e.g., Validate -> dst.Validate).
			// When stubs are enabled, the aliases handle backward
			// compatibility, so qualification is not needed.
			//
			// Methods and fields travel with their parent type and are
			// accessed through instances, not as bare identifiers.
			if id.Qualifier == nil && !(opts.stubsEnabled()) && !grp.Kind.TravelsWithType() {
				identOff := ix.Fset.Position(id.Ident.Pos()).Offset
				identEnd := identOff + len(id.Ident.Name)

				if movedSpans.Contains(filePath, identOff, identEnd) {
					continue // inside extracted code, handled during assembly
				}

				fe := ensureFile(filePath)
				tgtLocalName := guessImportLocalName(info.tgtPkgPath)
				fe.nameEdits = append(fe.nameEdits, edit{
					Start: identOff,
					End:   identEnd,
					New:   tgtLocalName + "." + info.tgtName,
				})
				fe.addImports[info.tgtPkgPath] = true
				continue
			}

			// Qualified cross-package consumer reference (pkg.Name).
			// When stubs are enabled, consumers keep using the source
			// package's names — the stubs redirect to the target.
			if id.Qualifier == nil || (opts.stubsEnabled()) {
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
		}
	}

	// Merge consumer edits into the rename set and import set.
	// Process files in sorted order for deterministic output.
	sortedConsumerFiles := sortedKeys(byFile)
	for _, filePath := range sortedConsumerFiles {
		fe := byFile[filePath]
		// Merge qualifier and name edits into renames.
		allEdits := make([]edit, 0, len(fe.qualifierEdits)+len(fe.nameEdits))
		allEdits = append(allEdits, fe.qualifierEdits...)
		allEdits = append(allEdits, fe.nameEdits...)
		allEdits = deduplicateEdits(allEdits)
		if len(allEdits) > 0 {
			// Consumer edits supersede rename edits at the same offset
			// (e.g., a source-file qualification edit replaces a plain
			// rename edit because it includes the qualifier prefix).
			consumerOffsets := make(map[int]bool)
			for _, e := range allEdits {
				consumerOffsets[e.Start] = true
			}
			var kept []edit
			for _, e := range renames.byFile[filePath] {
				if !consumerOffsets[e.Start] {
					kept = append(kept, e)
				}
			}
			renames.byFile[filePath] = append(kept, allEdits...)
			renames.byFile[filePath] = deduplicateEdits(renames.byFile[filePath])
		}

		// Add target imports in sorted order for deterministic output.
		sortedImports := sortedKeys(fe.addImports)
		for _, tgtPath := range sortedImports {
			ic := imports.ensureFile(filePath)

			// Check if the target import already exists in the file.
			f := ix.FilesByPath[filePath]
			if f != nil {
				alreadyImported := false
				for _, imp := range f.Syntax.Imports {
					impPath := importPath(imp)
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

		// Sort added imports for deterministic output.
		if ic := imports.byFile[filePath]; ic != nil {
			sort.Slice(ic.Add, func(i, j int) bool {
				return ic.Add[i].Path < ic.Add[j].Path
			})
		}
	}
}
