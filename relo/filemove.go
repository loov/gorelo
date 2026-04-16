package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// fileMoveInfo is Compile-internal state for a single FileMove: the resolved
// source mast.File plus the set of resolvedRelos synthesised from its
// top-level declarations.
type fileMoveInfo struct {
	move    FileMove
	srcFile *mast.File
	relos   []*resolvedRelo
}

// expandFileMoves resolves each FileMove against the index and produces one
// Relo per top-level declaration in the source file. If userRelos already
// targets an ident inside a moved file, synthesis skips that ident and the
// user's Relo is mutated in place to inherit the file-move destination.
func expandFileMoves(ix *mast.Index, moves []FileMove, userRelos []Relo) ([]Relo, []*fileMoveInfo, error) {
	if len(moves) == 0 {
		return nil, nil, nil
	}

	// First pass: validate each move and record its target.
	type moveCtx struct {
		info *fileMoveInfo
	}
	var ordered []moveCtx
	seenFrom := make(map[string]bool)
	seenTo := make(map[string]bool)
	// byFile maps absolute source path -> moveCtx so the user-relo
	// adjustment phase can look up the target for an ident's file.
	byFile := make(map[string]moveCtx)

	for _, m := range moves {
		src := lookupFile(ix, m.From)
		if src == nil {
			return nil, nil, fmt.Errorf("file-move source %q not found in the loaded packages", m.From)
		}
		if seenFrom[src.Path] {
			return nil, nil, fmt.Errorf("file-move source %q listed more than once", m.From)
		}
		seenFrom[src.Path] = true

		if m.To == "" {
			return nil, nil, fmt.Errorf("file-move destination must not be empty")
		}
		absTo, err := filepath.Abs(m.To)
		if err != nil {
			return nil, nil, fmt.Errorf("file-move destination %q: %w", m.To, err)
		}
		if absTo == src.Path {
			return nil, nil, fmt.Errorf("file-move source and destination are the same file: %s", src.Path)
		}
		if _, err := os.Stat(absTo); err == nil {
			return nil, nil, fmt.Errorf("file-move destination %q already exists; use per-declaration rules to merge", absTo)
		}
		if seenTo[absTo] {
			return nil, nil, fmt.Errorf("duplicate destination %q across file moves", absTo)
		}
		seenTo[absTo] = true

		info := &fileMoveInfo{move: FileMove{From: src.Path, To: absTo}, srcFile: src}
		ctx := moveCtx{info: info}
		ordered = append(ordered, ctx)
		byFile[src.Path] = ctx
	}

	// Adjust user relos that target an ident defined in a moved file:
	// inherit the file-move destination when MoveTo is empty.
	skipIdent := make(map[*ast.Ident]bool)
	for i := range userRelos {
		r := &userRelos[i]
		if r.Ident == nil {
			continue
		}
		defFile := defIdentFile(ix, r.Ident)
		if defFile == nil {
			continue
		}
		ctx, ok := byFile[defFile.Path]
		if !ok {
			continue
		}
		if r.MoveTo == "" {
			r.MoveTo = ctx.info.move.To
		}
		skipIdent[r.Ident] = true
	}

	// Synthesize a Relo per top-level decl, skipping any ident already
	// covered by an explicit user relo.
	var relos []Relo
	var infos []*fileMoveInfo
	for _, ctx := range ordered {
		src := ctx.info.srcFile
		for _, decl := range src.Syntax.Decls {
			for _, defIdent := range topLevelDefIdents(decl) {
				if defIdent == nil || skipIdent[defIdent] {
					continue
				}
				if ix.Group(defIdent) == nil {
					continue
				}
				relos = append(relos, Relo{Ident: defIdent, MoveTo: ctx.info.move.To})
			}
		}
		infos = append(infos, ctx.info)
	}

	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].move.From < infos[j].move.From
	})
	return relos, infos, nil
}

// defIdentFile returns the mast.File containing the def ident for id's group,
// or nil if the ident is not tracked or has no def.
func defIdentFile(ix *mast.Index, id *ast.Ident) *mast.File {
	grp := ix.Group(id)
	if grp == nil {
		return nil
	}
	for _, it := range grp.Idents {
		if it.Kind == mast.Def && it.File != nil {
			return it.File
		}
	}
	return nil
}

// topLevelDefIdents returns the defining identifiers for a top-level
// declaration. For FuncDecl this is the single name; for GenDecl it returns
// all name identifiers across every spec in the declaration (including every
// name in a ValueSpec with multiple names).
func topLevelDefIdents(decl ast.Decl) []*ast.Ident {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Name == nil {
			return nil
		}
		return []*ast.Ident{d.Name}
	case *ast.GenDecl:
		if d.Tok == token.IMPORT {
			return nil
		}
		var ids []*ast.Ident
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name != nil {
					ids = append(ids, s.Name)
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n != nil {
						ids = append(ids, n)
					}
				}
			}
		}
		return ids
	}
	return nil
}

// tagFileMoves attaches each resolvedRelo whose source file matches a file
// move's From path to that move's info so the assemble phase can recognise it.
func tagFileMoves(resolved []*resolvedRelo, infos []*fileMoveInfo) {
	if len(infos) == 0 {
		return
	}
	byFrom := make(map[string]*fileMoveInfo, len(infos))
	for _, info := range infos {
		byFrom[info.move.From] = info
	}
	for _, rr := range resolved {
		if rr.File == nil {
			continue
		}
		info, ok := byFrom[rr.File.Path]
		if !ok {
			continue
		}
		if rr.TargetFile != info.move.To {
			continue
		}
		rr.FromFileMove = info
		info.relos = append(info.relos, rr)
	}
}

// lookupFile resolves a user-supplied file path against the index. It accepts
// absolute paths, repo-relative paths, and the raw string stored in
// ix.FilesByPath.
func lookupFile(ix *mast.Index, path string) *mast.File {
	if f, ok := ix.FilesByPath[path]; ok {
		return f
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		if f, ok := ix.FilesByPath[abs]; ok {
			return f
		}
	}
	// Fall back to suffix match for convenience when callers pass a
	// partial path (rare; mostly for tests).
	for p, f := range ix.FilesByPath {
		if strings.HasSuffix(p, string(filepath.Separator)+path) {
			return f
		}
	}
	return nil
}

// assembleFileMoves emits FileEdits for every file move: the destination
// receives the source bytes with the package clause rewritten and per-decl
// qualification edits applied; the source path is scheduled for deletion.
// Per-decl Moves into the same destination (synthesized by
// emitCrossFileExtraction or method-follow expansion) append after this
// pre-rendered content via singlePassApply.
func (a *assembler) assembleFileMoves(infos []*fileMoveInfo) map[string]bool {
	handled := make(map[string]bool)
	if len(infos) == 0 {
		return handled
	}

	for _, info := range infos {
		src := info.srcFile
		if src == nil || src.Src == nil {
			continue
		}

		targetDir := filepath.Dir(info.move.To)
		targetPkgName := a.packageNameForDir(targetDir, src)
		crossPackage := targetPkgName != src.Syntax.Name.Name

		content := a.renderMovedFile(info, targetPkgName, crossPackage)

		a.out[info.move.To] = FileEdit{Path: info.move.To, IsNew: true, Content: content}
		a.out[src.Path] = FileEdit{Path: src.Path, IsDelete: true}

		handled[info.move.To] = true
		handled[src.Path] = true
	}
	return handled
}

// renderMovedFile produces the destination content for a file move by
// running a sub-Plan over the source bytes. The sub-Plan carries every
// per-decl extraction edit (qualification, self-import unqualification,
// alias fixups), every in-span detach/attach decl rewrite already in the
// shared a.edits Plan, and a package-clause Replace when the destination
// lives in a different package. Cross-target imports referenced by the
// moved decls are registered on the shared importSet so applyImportsPass
// installs them in the destination.
func (a *assembler) renderMovedFile(info *fileMoveInfo, targetPkgName string, crossPackage bool) string {
	src := info.srcFile
	srcPath := src.Path

	sub := &ed.Plan{}
	for _, rr := range info.relos {
		s := a.spans[rr]
		if s == nil {
			continue
		}
		for _, e := range rewriteSpanQualifiers(a.ix, rr, s, a.resolved, a.imports) {
			emitSpanRelativeAtAbs(sub, srcPath, s.Start, e, "filemove")
		}
	}

	// In-span primitives already on the shared Plan (detach/attach
	// decl rewrites, consumer renames inside a moved span) — replay
	// them onto the sub-Plan in absolute source coordinates.
	carryPlanInSpans(sub, a.edits, srcPath, info.relos, a.spans)

	if crossPackage {
		pkgName := src.Syntax.Name
		pkgOff := a.ix.Fset.Position(pkgName.Pos()).Offset
		pkgEnd := pkgOff + len(pkgName.Name)
		sub.Replace(ed.Span{Path: srcPath, Start: pkgOff, End: pkgEnd}, targetPkgName, "filemove-pkg")
	}

	dup := make([]byte, len(src.Src))
	copy(dup, src.Src)
	outputs, err := sub.Apply(map[string][]byte{srcPath: dup})
	if err != nil {
		a.plan.Warnings.Addf("filemove sub-plan failed for %s: %v", srcPath, err)
		return string(src.Src)
	}
	content := string(outputs[srcPath])
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content
}

// carryPlanInSpans copies primitives from src that target srcPath and
// fall inside any rr's span into dst. Move primitives are skipped (they
// belong to cross-file extraction, not whole-file rendering).
func carryPlanInSpans(dst, src *ed.Plan, srcPath string, relos []*resolvedRelo, spans map[*resolvedRelo]*span) {
	if len(relos) == 0 {
		return
	}
	for _, prim := range src.Primitives() {
		var pStart, pEnd int
		var path string
		switch x := prim.(type) {
		case ed.Insert:
			path, pStart, pEnd = x.Anchor.Path, x.Anchor.Offset, x.Anchor.Offset
		case ed.Delete:
			path, pStart, pEnd = x.Span.Path, x.Span.Start, x.Span.End
		case ed.Replace:
			path, pStart, pEnd = x.Span.Path, x.Span.Start, x.Span.End
		default:
			continue
		}
		if path != srcPath {
			continue
		}
		var inSpan bool
		for _, rr := range relos {
			s := spans[rr]
			if s == nil {
				continue
			}
			if pStart >= s.Start && pEnd <= s.End {
				inSpan = true
				break
			}
		}
		if !inSpan {
			continue
		}
		switch x := prim.(type) {
		case ed.Insert:
			dst.Insert(x.Anchor, x.Text, x.Side, "filemove-carry")
		case ed.Delete:
			dst.Delete(x.Span, "filemove-carry")
		case ed.Replace:
			dst.Replace(x.Span, x.Text, "filemove-carry")
		}
	}
}

// packageNameForDir finds the best package name to use for a target directory.
// If the directory matches the source file's directory, the source package
// name is used. Otherwise, existing mast packages rooted at that directory
// take precedence; the directory basename is the final fallback.
func (a *assembler) packageNameForDir(targetDir string, srcFile *mast.File) string {
	srcDir := filepath.Dir(srcFile.Path)
	if targetDir == srcDir {
		return srcFile.Syntax.Name.Name
	}
	for _, pkg := range a.ix.Pkgs {
		if len(pkg.Files) == 0 {
			continue
		}
		if strings.HasSuffix(pkg.Name, "_test") {
			continue
		}
		if filepath.Dir(pkg.Files[0].Path) == targetDir {
			return pkg.Name
		}
	}
	return guessPackageName(targetDir)
}
