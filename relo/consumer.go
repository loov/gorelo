package relo

import (
	"path/filepath"
	"sort"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// computeConsumerEdits emits qualifier-region edits for files that
// reference cross-package-moved groups. It only touches the qualifier
// region [qualStart, identStart); ident renames are handled by
// computeRenames on the non-overlapping ident region. Groups whose
// kind TravelsWithType (methods, fields) are skipped in external
// packages — they are accessed via receivers, not package qualifiers.
func computeConsumerEdits(ix *mast.Index, resolved []*resolvedRelo, movedSpans movedSpanIndex, edits *ed.Plan, imports *importSet, opts *Options) {
	type moveInfo struct {
		srcPkgPath string // source package import path
		tgtPkgPath string // target package import path
		tgtDir     string // target directory (absolute path)
	}

	// Groups that cannot rely on stubs: file-move groups (source file
	// is deleted) and detach/attach groups (calling convention changes,
	// so stubs don't provide the old access pattern).
	noStubGroups := make(map[*mast.Group]bool)
	for _, rr := range resolved {
		if rr.FromFileMove != nil || rr.Relo.Detach || rr.Relo.MethodOf != "" {
			noStubGroups[rr.Group] = true
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
		tgtDir := finalDir(rr)
		if groupTargetDirs[rr.Group] == nil {
			groupTargetDirs[rr.Group] = make(map[string]bool)
		}
		groupTargetDirs[rr.Group][tgtDir] = true

		if !rr.isCrossPackageMove() {
			continue
		}

		srcPkgPath := guessImportPath(filepath.Dir(rr.File.Path))
		tgtPkgPath := guessImportPath(tgtDir)
		if srcPkgPath == "" || tgtPkgPath == "" {
			continue
		}

		movedGroups[rr.Group] = &moveInfo{
			srcPkgPath: srcPkgPath,
			tgtPkgPath: tgtPkgPath,
			tgtDir:     tgtDir,
		}
	}

	if len(movedGroups) == 0 {
		return
	}

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

	for _, grp := range sortedGroups {
		info := movedGroups[grp]
		// When stubs are enabled and a source file exists to hold them,
		// consumers keep using the source package's names. File-move
		// groups always need rewriting because the source file is deleted.
		stubsHandled := opts.stubsEnabled() && !noStubGroups[grp]

		for _, id := range grp.Idents {
			if id.Kind != mast.Use || id.File == nil {
				continue
			}
			filePath := id.File.Path
			inTargetPkg := groupTargetDirs[grp][filepath.Dir(filePath)]
			identOff := ix.Fset.Position(id.Ident.Pos()).Offset
			identEnd := identOff + len(id.Ident.Name)
			qualified := id.Qualifier != nil

			if movedSpans.Contains(filePath, identOff, identEnd) {
				continue
			}

			switch {
			case inTargetPkg && !qualified:
				// Bare reference becoming local — no qualifier to edit.

			case inTargetPkg && qualified:
				// Qualified reference becoming local — strip qualifier.
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				selOff := ix.Fset.Position(id.Ident.Pos()).Offset
				emitEdit(edits, filePath, qualOff, selOff, "", "consumer-qualifier")

			case !inTargetPkg && !qualified:
				// Bare reference to declaration leaving the package —
				// insert package qualifier before the ident.
				if stubsHandled || grp.Kind.TravelsWithType() {
					continue
				}
				tgtLocalName := packageLocalName(ix, info.tgtDir)
				emitEdit(edits, filePath, identOff, identOff, tgtLocalName+".", "consumer-qualifier")
				addImportEntry(imports, ix, filePath, importEntry{Path: info.tgtPkgPath})

			case !inTargetPkg && qualified:
				// Qualified cross-package reference — requalify.
				if stubsHandled || grp.Kind.TravelsWithType() {
					continue
				}
				tgtLocalName := packageLocalName(ix, info.tgtDir)
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				qualEnd := qualOff + len(id.Qualifier.Name)
				if id.Qualifier.Name != tgtLocalName {
					emitEdit(edits, filePath, qualOff, qualEnd, tgtLocalName, "consumer-qualifier")
				}
				addImportEntry(imports, ix, filePath, importEntry{Path: info.tgtPkgPath})
			}
		}
	}

	// Sort each affected file's queued imports for deterministic output.
	for _, ic := range imports.byFile {
		sort.Slice(ic.Add, func(i, j int) bool {
			return ic.Add[i].Path < ic.Add[j].Path
		})
	}
}
