package relo

import (
	"path/filepath"
	"sort"

	"github.com/loov/gorelo/mast"
)

// computeConsumerEdits emits qualifier-region edits for files that
// reference cross-package-moved groups. It only touches the qualifier
// region [qualStart, identStart); ident renames are handled by
// computeRenames on the non-overlapping ident region. Groups whose
// kind TravelsWithType (methods, fields) are skipped in external
// packages — they are accessed via receivers, not package qualifiers.
func computeConsumerEdits(ctx *compileCtx) {
	ix, resolved := ctx.ix, ctx.resolved
	movedSpans, edits, imports, opts := ctx.movedSpans, ctx.edits, ctx.imports, ctx.opts

	// Groups that cannot rely on stubs: file-move groups (source file
	// is deleted) and detach/attach groups (calling convention changes,
	// so stubs don't provide the old access pattern).
	noStubGroups := make(map[*mast.Group]bool)
	for _, rr := range resolved {
		if rr.FromFileMove != nil || rr.Relo.Detach || rr.Relo.MethodOf != "" {
			noStubGroups[rr.Group] = true
		}
	}

	// Collect cross-package moved groups and their target directories.
	type groupInfo struct {
		targetDir string
	}
	movedGroups := make(map[*mast.Group]*groupInfo)

	for _, rr := range resolved {
		if !rr.isCrossPackageMove() {
			continue
		}
		movedGroups[rr.Group] = &groupInfo{targetDir: finalDir(rr)}
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
			identOff := ix.Fset.Position(id.Ident.Pos()).Offset
			identEnd := identOff + len(id.Ident.Name)
			qualified := id.Qualifier != nil

			if movedSpans.Contains(filePath, identOff, identEnd) {
				continue
			}

			qe := ctx.classifyRef(info.targetDir, filepath.Dir(filePath))

			if qe.LocalRef {
				if qualified {
					qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
					selOff := ix.Fset.Position(id.Ident.Pos()).Offset
					emitEdit(edits, filePath, qualOff, selOff, "", "consumer-qualifier")
				}
				continue
			}

			// External reference — stubs handle the rewrite for these groups.
			if stubsHandled || grp.Kind.TravelsWithType() {
				continue
			}

			if !qualified {
				emitEdit(edits, filePath, identOff, identOff, qe.Qualifier+".", "consumer-qualifier")
			} else {
				qualOff := ix.Fset.Position(id.Qualifier.Pos()).Offset
				qualEnd := qualOff + len(id.Qualifier.Name)
				if id.Qualifier.Name != qe.Qualifier {
					emitEdit(edits, filePath, qualOff, qualEnd, qe.Qualifier, "consumer-qualifier")
				}
			}
			if qe.ImportPath != "" {
				addImportEntry(imports, ix, filePath, importEntry{Path: qe.ImportPath})
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
