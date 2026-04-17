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

// isFileMoveSource reports whether every relo in rrs originated from
// a whole-file move, meaning the source file is being entirely
// relocated and should not generate stubs.
func isFileMoveSource(rrs []*resolvedRelo) bool {
	for _, rr := range rrs {
		if rr.FromFileMove == nil {
			return false
		}
	}
	return len(rrs) > 0
}

// emitFileMoveEdits emits qualification edits and package-clause
// Replaces for every file move onto the shared Plan. These primitives
// target the source file path; assembleFileMoves builds a sub-Plan by
// filtering them (along with detach/attach edits already on the shared
// Plan) to render each file-move target. The rendered content is
// pre-loaded as the target's input so per-decl Moves from other
// sources append after it via plan.Apply.
func emitFileMoveEdits(ix *mast.Index, infos []*fileMoveInfo, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, edits *ed.Plan, imports *importSet) {
	for _, info := range infos {
		src := info.srcFile
		if src == nil || src.Src == nil {
			continue
		}
		srcPath := src.Path

		for _, rr := range info.relos {
			s := spans[rr]
			if s == nil {
				continue
			}
			rewriteSpanQualifiers(edits, ix, rr, s, resolved, imports, "filemove")
		}

		targetDir := filepath.Dir(info.move.To)
		targetPkgName := fileMovePackageName(ix, targetDir, src)
		if targetPkgName != src.Syntax.Name.Name {
			pkgName := src.Syntax.Name
			pkgOff := ix.Fset.Position(pkgName.Pos()).Offset
			pkgEnd := pkgOff + len(pkgName.Name)
			edits.Replace(ed.Span{Path: srcPath, Start: pkgOff, End: pkgEnd}, targetPkgName, "filemove-pkg")
		}
	}
}

// assembleFileMoves renders each file-move target by extracting all
// non-Move primitives targeting the source file from the shared Plan
// and applying them to the source bytes. The rendered content is
// stored in a.out so gatherInputs can pre-load it as the target's
// input (allowing per-decl Moves to append via the main plan.Apply).
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
		srcPath := src.Path

		sub := filterPlanForFile(a.edits, srcPath)

		dup := make([]byte, len(src.Src))
		copy(dup, src.Src)
		outputs, err := sub.Apply(map[string][]byte{srcPath: dup})
		if err != nil {
			a.plan.Warnings.Addf("filemove sub-plan failed for %s: %v", srcPath, err)
			a.out[info.move.To] = FileEdit{Path: info.move.To, IsNew: true, Content: string(src.Src)}
		} else {
			content := string(outputs[srcPath])
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			a.out[info.move.To] = FileEdit{Path: info.move.To, IsNew: true, Content: content}
		}
		a.out[srcPath] = FileEdit{Path: srcPath, IsDelete: true}

		handled[info.move.To] = true
		handled[srcPath] = true
	}
	return handled
}

// filterPlanForFile builds a sub-Plan containing only the non-Move
// primitives from shared that target path. Move primitives are
// skipped: per-decl extraction Moves are handled by the main
// plan.Apply pass, not by the file-move rendering.
func filterPlanForFile(shared *ed.Plan, path string) *ed.Plan {
	sub := &ed.Plan{}
	for _, prim := range shared.Primitives() {
		switch x := prim.(type) {
		case ed.Insert:
			if x.Anchor.Path == path {
				sub.Insert(x.Anchor, x.Text, x.Side, x.Origin())
			}
		case ed.Delete:
			if x.Span.Path == path {
				sub.Delete(x.Span, x.Origin())
			}
		case ed.Replace:
			if x.Span.Path == path {
				sub.Replace(x.Span, x.Text, x.Origin())
			}
		}
	}
	return sub
}

// fileMovePackageName determines the package name for a file-move
// target directory. If the target is in the same directory as the
// source, the source's package name is used. Otherwise, existing
// packages in the target directory take precedence; the directory
// basename is the final fallback.
func fileMovePackageName(ix *mast.Index, targetDir string, srcFile *mast.File) string {
	if targetDir == filepath.Dir(srcFile.Path) {
		return srcFile.Syntax.Name.Name
	}
	if pkg := findPkgForDir(ix, targetDir); pkg != nil {
		return pkg.Name
	}
	return guessPackageName(targetDir)
}
