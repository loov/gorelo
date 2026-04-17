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
func computeConsumerEdits(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, movedSpans movedSpanIndex, detachGroups map[*mast.Group]bool, edits *ed.Plan, imports *importSet, opts *Options, plan *Plan) {
	type moveInfo struct {
		srcPkgPath string // source package import path
		tgtPkgPath string // target package import path
		tgtDir     string // target directory (absolute path)
		tgtName    string // name at the target (may differ if renamed)
	}

	// Groups originating from a whole-file move cannot rely on stub
	// aliases because the source file is deleted — consumers must always
	// be rewritten even when @stubs is enabled.
	fileMoveGroups := make(map[*mast.Group]bool)
	for _, rr := range resolved {
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
			tgtName:    rr.TargetName,
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
		if detachGroups[grp] {
			continue
		}
		info := movedGroups[grp]
		// When stubs are enabled and a source file exists to hold them,
		// consumers keep using the source package's names. File-move
		// groups always need rewriting because the source file is deleted.
		stubsHandled := opts.stubsEnabled() && !fileMoveGroups[grp]

		for _, id := range grp.Idents {
			if id.Kind != mast.Use || id.File == nil {
				continue
			}
			filePath := id.File.Path
			inTargetPkg := groupTargetDirs[grp][filepath.Dir(filePath)]
			identOff := ix.Fset.Position(id.Ident.Pos()).Offset
			identEnd := identOff + len(id.Ident.Name)
			qualified := id.Qualifier != nil
			renamed := info.tgtName != grp.Name

			if movedSpans.Contains(filePath, identOff, identEnd) {
				continue
			}

			switch {
			case inTargetPkg && !qualified:
				// Bare reference becoming local — rename if needed.
				if renamed {
					emitEdit(edits, filePath, identOff, identEnd, info.tgtName, "consumer-name")
				}

			case inTargetPkg && qualified:
				// Qualified reference becoming local — strip qualifier.
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				selOff := ix.Fset.Position(id.Ident.Pos()).Offset
				emitEdit(edits, filePath, qualOff, selOff, "", "consumer-qualifier")
				if renamed {
					emitEdit(edits, filePath, selOff, selOff+len(id.Ident.Name), info.tgtName, "consumer-name")
				}

			case !inTargetPkg && !qualified:
				// Bare reference to declaration leaving the package.
				if stubsHandled || grp.Kind.TravelsWithType() {
					continue
				}
				tgtLocalName := packageLocalName(ix, info.tgtDir)
				emitEdit(edits, filePath, identOff, identEnd, tgtLocalName+"."+info.tgtName, "consumer-name")
				addImportEntry(imports, ix, filePath, importEntry{Path: info.tgtPkgPath})

			case !inTargetPkg && qualified:
				// Qualified cross-package reference — requalify.
				if stubsHandled {
					continue
				}
				tgtLocalName := packageLocalName(ix, info.tgtDir)
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				qualEnd := qualOff + len(id.Qualifier.Name)
				if id.Qualifier.Name != tgtLocalName {
					emitEdit(edits, filePath, qualOff, qualEnd, tgtLocalName, "consumer-qualifier")
				}
				if renamed {
					emitEdit(edits, filePath, identOff, identEnd, info.tgtName, "consumer-name")
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
