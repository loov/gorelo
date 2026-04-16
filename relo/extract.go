package relo

import (
	"bytes"
	"path/filepath"
	"sort"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// emitCrossFileExtraction emits the Plan primitives that move each
// cross-file extracted span to its target file: a Move per unique
// source span (with appropriate GroupRender so the destination text
// is wrapped/separated correctly), plus carried Insert/Delete/Replace
// primitives in the source span for the qualification rewrites
// (renames, cross-target package qualifications, self-import
// removals, import-alias rewrites). Cross-target imports/aliases
// discovered during the walk are added to the importSet so that
// applyImportsPass can install them in the destination file.
//
// File-move-synthesized rrs are skipped — assembleFileMoves owns
// their rendering via a sub-Plan.
func emitCrossFileExtraction(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, edits *ed.Plan, imports *importSet) {
	type spanKey struct {
		path       string
		start, end int
	}
	emittedSpan := make(map[spanKey]bool)

	for _, rr := range resolved {
		if !rr.isCrossFileMove() {
			continue
		}
		if rr.File == nil {
			continue
		}
		// File-move-synthesized rrs are handled by assembleFileMoves,
		// not by the main plan.Apply pass; their source file isn't in
		// inputs so emitting a Move here would be out-of-bounds.
		if rr.FromFileMove != nil {
			continue
		}
		s := spans[rr]
		if s == nil {
			continue
		}
		srcPath := rr.File.Path
		targetPath := rr.TargetFile

		// Emit qualification edits (computeExtractedEdits returns
		// span-relative; convert to absolute coords on the source file
		// so the edits land inside the Move's span and get carried to
		// the destination automatically).
		er := computeExtractedEdits(ix, rr, s, resolved)
		for _, e := range er.edits {
			emitSpanRelativeAtAbs(edits, srcPath, s.Start, e, "extract-qualify")
		}

		// Self-import unqualification inside the span.
		targetDir := filepath.Dir(targetPath)
		if targetImportPath := guessImportPath(targetDir); targetImportPath != "" {
			for _, e := range collectSelfImportEdits(ix, rr, s, targetImportPath, resolved) {
				emitSpanRelativeAtAbs(edits, srcPath, s.Start, e, "extract-self-import")
			}
		}

		// Import-alias rewrites inside the span (alias collisions
		// resolved in computeImports / addImportEntry).
		if ic := imports.byFile[targetPath]; ic != nil {
			for _, e := range computeImportAliasEdits(ix, rr, s, ic) {
				emitSpanRelativeAtAbs(edits, srcPath, s.Start, e, "extract-alias")
			}
		}

		// Register cross-target imports for applyImportsPass.
		for impPath := range er.imports {
			entry := importEntry{Path: impPath}
			if alias, ok := er.aliases[impPath]; ok {
				entry.Alias = alias
			}
			addImportEntry(imports, ix, targetPath, entry)
		}

		// Emit the Move once per unique source span (multi-name decls
		// like `const A, B = 1, 2` yield multiple rrs sharing one span).
		key := spanKey{srcPath, s.Start, s.End}
		if emittedSpan[key] {
			continue
		}
		emittedSpan[key] = true

		opts := ed.MoveOptions{Dedent: s.IsGrouped}
		if s.IsGrouped {
			opts.GroupKeyword = s.Keyword
			opts.GroupRender = goBlockRenderer(s.Keyword)
		} else {
			opts.GroupRender = goItemRenderer()
		}
		edits.Move(
			ed.Span{Path: srcPath, Start: s.Start, End: s.End},
			ed.Anchor{Path: targetPath, Offset: -1},
			opts,
			"extract",
		)
	}
}

// emitSpanRelativeAtAbs emits a single span-relative edit as the
// equivalent absolute-coord Plan primitive on srcPath. Used to lower
// the span-relative outputs of computeExtractedEdits /
// collectSelfImportEdits / computeImportAliasEdits into primitives
// that ride along with the enclosing Move (or a sub-Plan for
// whole-file moves).
func emitSpanRelativeAtAbs(edits *ed.Plan, srcPath string, spanStart int, e edit, origin string) {
	absStart := spanStart + e.Start
	absEnd := spanStart + e.End
	switch {
	case absStart == absEnd:
		edits.Insert(ed.Anchor{Path: srcPath, Offset: absStart}, e.New, ed.Before, origin)
	case e.New == "":
		edits.Delete(ed.Span{Path: srcPath, Start: absStart, End: absEnd}, origin)
	default:
		edits.Replace(ed.Span{Path: srcPath, Start: absStart, End: absEnd}, e.New, origin)
	}
}

// goBlockRenderer returns an edit.GroupRenderer that wraps a same-keyword
// run of items in Go's `keyword (\n\t…\n)\n` block form, or in the
// inline `keyword X` form when there's a single item. The single-item
// form inserts the keyword before the first non-comment line so that
// any leading doc comment stays above the keyword. The renderer
// prepends a leading newline so consecutive groups at one destination
// are visually separated.
func goBlockRenderer(kw string) ed.GroupRenderer {
	return func(items [][]byte) []byte {
		if len(items) == 1 {
			body := bytes.TrimRight(items[0], "\n")
			return []byte("\n" + prependKeyword(string(body), kw) + "\n")
		}
		var b bytes.Buffer
		b.WriteString("\n" + kw + " (\n")
		for _, item := range items {
			body := bytes.TrimRight(item, "\n")
			for _, line := range bytes.Split(body, []byte{'\n'}) {
				b.WriteByte('\t')
				b.Write(line)
				b.WriteByte('\n')
			}
		}
		b.WriteString(")\n")
		return b.Bytes()
	}
}

// goItemRenderer returns the GroupRenderer used for non-grouped
// declarations (empty GroupKeyword): each item becomes its own
// `\n<text>\n` block so adjacent items at the same destination are
// separated by a blank line.
func goItemRenderer() ed.GroupRenderer {
	return func(items [][]byte) []byte {
		var b bytes.Buffer
		for _, item := range items {
			body := bytes.TrimRight(item, "\n")
			b.WriteByte('\n')
			b.Write(body)
			b.WriteByte('\n')
		}
		return b.Bytes()
	}
}

// planEditPaths returns the sorted set of file paths referenced by any
// primitive in p.
func planEditPaths(p *ed.Plan) []string {
	seen := make(map[string]bool)
	for _, prim := range p.Primitives() {
		switch x := prim.(type) {
		case ed.Insert:
			seen[x.Anchor.Path] = true
		case ed.Delete:
			seen[x.Span.Path] = true
		case ed.Replace:
			seen[x.Span.Path] = true
		case ed.Move:
			seen[x.Span.Path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
