package relo

import (
	"path/filepath"
	"sort"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// computeConsumerEdits finds files in the index that reference moved groups
// from external packages and generates edits to update their qualifier
// expressions and imports. Edits are emitted onto the shared edits Plan;
// import additions go into the importSet so that the assembly phase
// applies them uniformly.
func computeConsumerEdits(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, edits *ed.Plan, imports *importSet, opts *Options, plan *Plan) {
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

	// Build moved span lookup so we can skip idents inside extracted code.
	movedSpans := buildMovedSpanIndex(resolved, spans)

	// Iterate movedGroups in a stable order so addImportEntry's
	// first-come-first-served alias collision resolution gives
	// deterministic results across runs.
	sortedGroups := make([]*mast.Group, 0, len(movedGroups))
	for grp := range movedGroups {
		sortedGroups = append(sortedGroups, grp)
	}
	sort.Slice(sortedGroups, func(i, j int) bool {
		if sortedGroups[i].Pkg != sortedGroups[j].Pkg {
			return sortedGroups[i].Pkg < sortedGroups[j].Pkg
		}
		return sortedGroups[i].Name < sortedGroups[j].Name
	})

	emit := func(filePath string, e edit, origin string) {
		switch {
		case e.Start == e.End:
			edits.Insert(ed.Anchor{Path: filePath, Offset: e.Start}, e.New, ed.Before, origin)
		case e.New == "":
			edits.Delete(ed.Span{Path: filePath, Start: e.Start, End: e.End}, origin)
		default:
			edits.Replace(ed.Span{Path: filePath, Start: e.Start, End: e.End}, e.New, origin)
		}
	}

	for _, grp := range sortedGroups {
		if detachGroups[grp] {
			continue
		}
		info := movedGroups[grp]
		for _, id := range grp.Idents {
			if id.Kind != mast.Use || id.File == nil {
				continue
			}
			filePath := id.File.Path
			inTargetPkg := groupTargetDirs[grp][filepath.Dir(filePath)]
			identOff := ix.Fset.Position(id.Ident.Pos()).Offset
			identEnd := identOff + len(id.Ident.Name)

			// File is in the target package: the declaration is
			// becoming local. Unqualify qualified references (e.g.,
			// src.Greet -> Greet) and apply renames if needed.
			if inTargetPkg {
				if id.Qualifier == nil {
					if info.tgtName == grp.Name {
						continue
					}
					if movedSpans.Contains(filePath, identOff, identEnd) {
						continue
					}
					emit(filePath, edit{Start: identOff, End: identEnd, New: info.tgtName}, "consumer-name")
					continue
				}
				// Qualified reference (e.g., src.Greet): remove qualifier.
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				selOff := ix.Fset.Position(id.Ident.Pos()).Offset
				emit(filePath, edit{Start: qualOff, End: selOff, New: ""}, "consumer-qualifier")
				if info.tgtName != grp.Name {
					emit(filePath, edit{Start: selOff, End: selOff + len(id.Ident.Name), New: info.tgtName}, "consumer-name")
				}
				continue
			}

			// Unqualified same-package reference: the declaration is
			// moving to a different package, so we need to add a
			// package qualifier (e.g., Validate -> dst.Validate).
			// When stubs are enabled, the aliases handle backward
			// compatibility, so qualification is not needed — unless
			// the group comes from a whole-file move, which leaves no
			// source file to hold the stubs. Methods and fields travel
			// with their parent type and are accessed through
			// instances, not as bare identifiers.
			stubsApply := opts.stubsEnabled() && !fileMoveGroups[grp]
			if id.Qualifier == nil && !stubsApply && !grp.Kind.TravelsWithType() {
				if movedSpans.Contains(filePath, identOff, identEnd) {
					continue // inside extracted code, handled during assembly
				}
				tgtLocalName := packageLocalName(ix, info.tgtDir)
				emit(filePath, edit{
					Start: identOff,
					End:   identEnd,
					New:   tgtLocalName + "." + info.tgtName,
				}, "consumer-name")
				addImportEntry(imports, ix, filePath, importEntry{Path: info.tgtPkgPath})
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
			if movedSpans.Contains(filePath, identOff, identEnd) {
				continue
			}

			tgtLocalName := packageLocalName(ix, info.tgtDir)
			qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
			qualEnd := qualOff + len(id.Qualifier.Name)
			if id.Qualifier.Name != tgtLocalName {
				emit(filePath, edit{Start: qualOff, End: qualEnd, New: tgtLocalName}, "consumer-qualifier")
			}
			if info.tgtName != grp.Name {
				emit(filePath, edit{Start: identOff, End: identEnd, New: info.tgtName}, "consumer-name")
			}
			addImportEntry(imports, ix, filePath, importEntry{Path: info.tgtPkgPath})
		}
	}

	// Sort each affected file's queued imports for deterministic output.
	for _, ic := range imports.byFile {
		sort.Slice(ic.Add, func(i, j int) bool {
			return ic.Add[i].Path < ic.Add[j].Path
		})
	}
}
