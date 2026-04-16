package relo

import (
	"path/filepath"
	"sort"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// emitConsumerEdit translates a consumer-built {Start,End,New} edit into
// the equivalent Plan primitive on path.
func emitConsumerEdit(edits *ed.Plan, path string, e edit, origin string) {
	switch {
	case e.Start == e.End:
		edits.Insert(ed.Anchor{Path: path, Offset: e.Start}, e.New, ed.Before, origin)
	case e.New == "":
		edits.Delete(ed.Span{Path: path, Start: e.Start, End: e.End}, origin)
	default:
		edits.Replace(ed.Span{Path: path, Start: e.Start, End: e.End}, e.New, origin)
	}
}

// computeConsumerEdits finds files in the index that reference moved groups
// from external packages and generates edits to update their qualifier
// expressions and imports. Edits are emitted onto the shared edits Plan;
// import additions go into the importSet so that the assembly phase
// applies them uniformly.
func computeConsumerEdits(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, edits *ed.Plan, imports *importSet, opts *Options, plan *Plan) {
	// Build lookup structures.
	type moveInfo struct {
		srcPkgPath string // source package import path
		tgtPkgPath string // target package import path
		tgtDir     string // target directory (absolute path)
		tgtName    string // name at the target (may differ if renamed)
	}

	// Groups with detach/attach handle their own cross-package qualification.
	detachGroups := make(map[*mast.Group]bool)
	// Groups originating from a whole-file move cannot rely on stub
	// aliases because the source file is deleted — consumers must always
	// be rewritten even when @stubs is enabled.
	fileMoveGroups := make(map[*mast.Group]bool)
	for _, rr := range resolved {
		if rr.Relo.Detach || rr.Relo.MethodOf != "" {
			detachGroups[rr.Group] = true
		}
		if rr.FromFileMove != nil {
			fileMoveGroups[rr.Group] = true
		}
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
			tgtDir:     tgtDir,
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
		addImports     map[string]string // target import path -> target dir
	}
	byFile := make(map[string]*fileEdits)

	ensureFile := func(path string) *fileEdits {
		fe, ok := byFile[path]
		if !ok {
			fe = &fileEdits{
				addImports: make(map[string]string),
			}
			byFile[path] = fe
		}
		return fe
	}

	// Build moved span lookup so we can skip idents inside extracted code.
	movedSpans := buildMovedSpanIndex(resolved, spans)

	for grp, info := range movedGroups {
		if detachGroups[grp] {
			continue
		}
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
			// compatibility, so qualification is not needed — unless
			// the group comes from a whole-file move, which leaves no
			// source file to hold the stubs.
			//
			// Methods and fields travel with their parent type and are
			// accessed through instances, not as bare identifiers.
			stubsApply := opts.stubsEnabled() && !fileMoveGroups[grp]
			if id.Qualifier == nil && !stubsApply && !grp.Kind.TravelsWithType() {
				identOff := ix.Fset.Position(id.Ident.Pos()).Offset
				identEnd := identOff + len(id.Ident.Name)

				if movedSpans.Contains(filePath, identOff, identEnd) {
					continue // inside extracted code, handled during assembly
				}

				fe := ensureFile(filePath)
				tgtLocalName := packageLocalName(ix, info.tgtDir)
				fe.nameEdits = append(fe.nameEdits, edit{
					Start: identOff,
					End:   identEnd,
					New:   tgtLocalName + "." + info.tgtName,
				})
				fe.addImports[info.tgtPkgPath] = info.tgtDir
				continue
			}

			// Qualified cross-package consumer reference (pkg.Name).
			// When stubs are enabled, consumers keep using the source
			// package's names — the stubs redirect to the target.
			// File-move groups still need rewriting because the source
			// file (and any stubs that would have lived in it) is gone.
			if id.Qualifier == nil || (opts.stubsEnabled() && !fileMoveGroups[grp]) {
				continue
			}

			// Skip references inside extracted code; the qualifier
			// rewrite there is handled by emitCrossFileExtraction.
			identOff := ix.Fset.Position(id.Ident.Pos()).Offset
			identEnd := identOff + len(id.Ident.Name)
			if movedSpans.Contains(filePath, identOff, identEnd) {
				continue
			}

			fe := ensureFile(filePath)

			// Determine what the new qualifier text should be.
			// Use the actual package name from the index when available.
			tgtLocalName := packageLocalName(ix, info.tgtDir)

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
				fe.nameEdits = append(fe.nameEdits, edit{
					Start: identOff,
					End:   identEnd,
					New:   info.tgtName,
				})
			}

			fe.addImports[info.tgtPkgPath] = info.tgtDir
		}
	}

	// Emit consumer edits onto the shared Plan and add target imports.
	// Process files in sorted order for deterministic output. Note that
	// computeRenames already skips cross-package-moved groups, so the
	// qualifier/name edits below have no overlapping rename emissions to
	// supersede.
	sortedConsumerFiles := sortedKeys(byFile)
	for _, filePath := range sortedConsumerFiles {
		fe := byFile[filePath]
		for _, e := range fe.qualifierEdits {
			emitConsumerEdit(edits, filePath, e, "consumer-qualifier")
		}
		for _, e := range fe.nameEdits {
			emitConsumerEdit(edits, filePath, e, "consumer-name")
		}

		// Register target imports in sorted order. addImportEntry
		// dedups against the destination's existing+queued imports
		// and auto-sets aliases when the real pkg name differs from
		// the path basename.
		for _, tgtPath := range sortedKeys(fe.addImports) {
			addImportEntry(imports, ix, filePath, importEntry{Path: tgtPath})
		}

		// Sort added imports for deterministic output.
		if ic := imports.byFile[filePath]; ic != nil {
			sort.Slice(ic.Add, func(i, j int) bool {
				return ic.Add[i].Path < ic.Add[j].Path
			})
		}
	}
}
