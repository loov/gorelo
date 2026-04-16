package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// assembleFileMoves emits FileEdits for every file move: the destination file
// receives the source bytes with the package clause rewritten and the import
// block rebuilt; the source path is scheduled for deletion.
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

		// Target package name: match existing files in the target dir, or
		// fall back to the directory name.
		targetDir := filepath.Dir(info.move.To)
		targetPkgName := a.packageNameForDir(targetDir, src)
		crossPackage := targetPkgName != src.Syntax.Name.Name

		content := a.renderMovedFile(info, targetPkgName, crossPackage)

		a.es.Set(FileEdit{Path: info.move.To, IsNew: true, Content: content})
		a.es.Set(FileEdit{Path: src.Path, IsDelete: true})

		handled[info.move.To] = true
		handled[src.Path] = true
	}
	return handled
}

// renderMovedFile produces the destination content for a file move.
func (a *assembler) renderMovedFile(info *fileMoveInfo, targetPkgName string, crossPackage bool) string {
	src := info.srcFile
	content := string(src.Src)

	// Apply per-decl extracted edits (cross-target qualification, rename
	// edits inside spans, import-alias fixups, self-import unqualification)
	// at absolute source offsets via applyEditsViaPlan, which sorts and
	// drops contained overlaps before running plan.Apply.
	var absEdits []edit
	ic := a.imports.byFile[info.move.To]
	targetImportPath := guessImportPath(filepath.Dir(info.move.To))

	// Collect new imports referenced by the moved decls that must be
	// added to the destination file (e.g., source-package imports for
	// references that now cross a package boundary).
	addImports := make(map[string]string) // importPath -> alias

	for _, rr := range info.relos {
		s := a.spans[rr]
		if s == nil {
			continue
		}
		er := computeExtractedEdits(a.ix, rr, s, a.resolved)
		for _, e := range er.edits {
			absEdits = append(absEdits, edit{
				Start: s.Start + e.Start,
				End:   s.Start + e.End,
				New:   e.New,
			})
		}
		// Detach/attach decl edits live in renames.byFile and are picked
		// up by the in-span filter below.
		for impPath := range er.imports {
			if _, ok := addImports[impPath]; !ok {
				addImports[impPath] = er.aliases[impPath]
			}
		}
		if targetImportPath != "" {
			for _, e := range collectSelfImportEdits(a.ix, rr, s, targetImportPath, a.resolved) {
				absEdits = append(absEdits, edit{
					Start: s.Start + e.Start,
					End:   s.Start + e.End,
					New:   e.New,
				})
			}
		}
		for _, e := range computeImportAliasEdits(a.ix, rr, s, ic) {
			absEdits = append(absEdits, edit{
				Start: s.Start + e.Start,
				End:   s.Start + e.End,
				New:   e.New,
			})
		}
		// Pull in any in-span Plan edits for this source path (detach
		// decl rewrites, consumer rename edits inside the moved span)
		// in absolute coordinates so they apply alongside the
		// absEdits already collected.
		for _, e := range planEditsForFile(a.edits, src.Path) {
			if e.Start >= s.Start && e.End <= s.End {
				absEdits = append(absEdits, e)
			}
		}
	}
	content = applyEditsViaPlan(a.plan, src.Path, []byte(content), absEdits)

	// Rewrite the package clause if the destination lives in a different
	// package. The assembled span above did not touch the package decl,
	// so this is a straightforward find-and-replace on the first "package"
	// line.
	if crossPackage {
		content = rewritePackageClause(content, targetPkgName)
	}

	// Cross-target imports collected from computeExtractedEdits
	// (e.g., a reference to a sibling decl that stayed behind in the
	// source package) are registered for applyImportsPass.
	for _, impPath := range sortedKeys(addImports) {
		if impPath == targetImportPath {
			continue
		}
		addImportEntry(a.imports, a.ix, info.move.To, importEntry{
			Path: impPath, Alias: addImports[impPath],
		})
	}

	// If the moved file referenced its own origin package through a
	// self-import, drop that import from the output.
	srcImportPath := guessImportPath(filepath.Dir(src.Path))
	if srcImportPath != "" && srcImportPath != targetImportPath {
		// Any siblings the moved file referenced unqualified now live in
		// a different package. Add an import for the origin if there are
		// consumer edits that qualified them.
	}

	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content
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

// rewritePackageClause replaces the first "package <name>" line with the
// given package name. It keeps any build-constraint header untouched.
func rewritePackageClause(src, newName string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "package ") {
			continue
		}
		// Preserve trailing comments (e.g., "package foo // docs").
		rest := strings.TrimPrefix(trimmed, "package ")
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) == 2 {
			lines[i] = "package " + newName + " " + parts[1]
		} else {
			lines[i] = "package " + newName
		}
		return strings.Join(lines, "\n")
	}
	return src
}
