package relo

import (
	"go/ast"
	"go/token"
	"maps"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/loov/gorelo/mast"
)

// importChange describes import modifications needed for a file.
//
// Existing is loaded lazily from the file's parsed AST on the first call
// to addImportEntry; Add accumulates new imports; Aliases records any
// alias assignments the collision-resolution path made for added entries
// (used by rewriteSpanQualifiers to look up the destination's local name
// for an import path when emitting qualifier-rewrite edits).
type importChange struct {
	Existing       []importEntry
	existingLoaded bool
	Add            []importEntry
	Aliases        map[string]string
}

// importEntry is a single import to add.
type importEntry struct {
	Path  string
	Alias string
}

// importSet holds import changes organized by file.
type importSet struct {
	byFile map[string]*importChange
}

// computeImports determines import changes for all affected files (phase 7).
func computeImports(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, plan *Plan) *importSet {
	is := &importSet{
		byFile: make(map[string]*importChange),
	}

	byTarget := groupByTarget(resolved)

	// For each target file, collect imports needed by moved declarations.
	for targetFile, rrs := range byTarget {
		targetDir := filepath.Dir(targetFile)

		// Collect imports used by declarations being moved to this target.
		neededImports := make(map[string]*ast.ImportSpec) // importPath -> spec
		for _, rr := range rrs {
			if rr.File == nil {
				continue
			}
			s := spans[rr]
			if s == nil {
				continue
			}

			// Walk the AST within the span to find selector expressions.
			usedIdents := make(map[string]bool)
			walkRange(rr.File.Syntax, ix.Fset, s.Start, s.End, func(n ast.Node) {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return
				}
				if ident, ok := sel.X.(*ast.Ident); ok {
					usedIdents[ident.Name] = true
				}
			})

			// Match against file imports.
			for _, imp := range rr.File.Syntax.Imports {
				impPath := importPath(imp)
				localName := importLocalName(imp, impPath)
				if localName == "." {
					plan.Warnings.AddAtf(rr, ix,
						"moved decl %s uses dot import %s which cannot be automatically transferred",
						rr.Group.Name, imp.Path.Value)
					continue
				}
				if localName == "_" {
					plan.Warnings.AddAtf(rr, ix,
						"moved decl %s has blank import %s for side effects which cannot be automatically transferred",
						rr.Group.Name, imp.Path.Value)
					continue
				}
				if usedIdents[localName] {
					neededImports[impPath] = imp
				}
			}
		}

		// Self-import elimination: if moving into a package, remove that
		// package's own import.
		targetImportPath := guessImportPath(targetDir)
		if targetImportPath != "" {
			delete(neededImports, targetImportPath)
		}

		// Build import entries for the target file.
		var entries []importEntry
		usedNames := make(map[string]bool)

		// Pre-populate usedNames with existing imports in the target file.
		if existingFile := ix.FilesByPath[targetFile]; existingFile != nil {
			for _, imp := range existingFile.Syntax.Imports {
				impPath := importPath(imp)
				usedNames[importLocalName(imp, impPath)] = true
			}
		}

		// First pass: collect all local names.
		var infos []importInfo
		for impPath, spec := range neededImports {
			localName := importLocalName(spec, impPath)
			infos = append(infos, importInfo{path: impPath, localName: localName, spec: spec})
			usedNames[localName] = true
		}
		sort.Slice(infos, func(i, j int) bool {
			return infos[i].path < infos[j].path
		})

		// Detect and resolve collisions.
		aliases := resolveCollisions(infos, usedNames)

		for _, info := range infos {
			entry := importEntry{Path: info.path}
			if alias, ok := aliases[info.path]; ok {
				entry.Alias = alias
			} else if info.spec.Name != nil && info.spec.Name.Name != path.Base(info.path) {
				entry.Alias = info.spec.Name.Name
			}
			entries = append(entries, entry)
		}

		if len(entries) > 0 {
			ic := is.ensureFile(targetFile)
			ic.Add = append(ic.Add, entries...)
			if len(aliases) > 0 {
				if ic.Aliases == nil {
					ic.Aliases = make(map[string]string)
				}
				maps.Copy(ic.Aliases, aliases)
			}
		}
	}

	return is
}

// addImportEntry registers entry as an import to add to filePath. On the
// first call for a file, the file's pre-existing imports are loaded
// from the index into ic.Existing. The entry is deduplicated against
// both Existing and Add by import path; if its desired local name
// (entry.Alias or guessed from path) collides with a different
// already-known import, a parent-prefixed alias is assigned and
// recorded in ic.Aliases. applyImportsPass picks the entries up after
// assembly.
func addImportEntry(is *importSet, ix *mast.Index, filePath string, entry importEntry) {
	ic := is.ensureFile(filePath)
	loadExistingImports(ic, ix, filePath)

	used := make(map[string]string, len(ic.Existing)+len(ic.Add))
	addToUsed := func(e importEntry) {
		used[importEntryLocalName(e)] = e.Path
	}
	for _, e := range ic.Existing {
		addToUsed(e)
	}
	for _, e := range ic.Add {
		addToUsed(e)
	}

	desired := importEntryLocalName(entry)
	if owner, exists := used[desired]; exists {
		if owner == entry.Path {
			return // already imported (existing or queued)
		}
		alias := parentPrefixedName(entry.Path)
		if alias == "" || alias == desired {
			alias = desired
		}
		if _, taken := used[alias]; taken {
			base := alias
			for i := 2; ; i++ {
				alias = base + strconv.Itoa(i)
				if _, taken := used[alias]; !taken {
					break
				}
			}
		}
		entry.Alias = alias
		if ic.Aliases == nil {
			ic.Aliases = make(map[string]string)
		}
		ic.Aliases[entry.Path] = alias
	}
	ic.Add = append(ic.Add, entry)
}

// loadExistingImports populates ic.Existing from the file's parsed
// imports (once per file).
func loadExistingImports(ic *importChange, ix *mast.Index, filePath string) {
	if ic.existingLoaded {
		return
	}
	ic.existingLoaded = true
	if ix == nil {
		return
	}
	file := ix.FilesByPath[filePath]
	if file == nil {
		return
	}
	for _, imp := range file.Syntax.Imports {
		impPath := importPath(imp)
		ie := importEntry{Path: impPath}
		if imp.Name != nil {
			ie.Alias = imp.Name.Name
		}
		ic.Existing = append(ic.Existing, ie)
	}
}

// importEntryLocalName returns the local name an import entry will be
// known by in the file (its alias if non-empty, else the guessed name).
func importEntryLocalName(e importEntry) string {
	if e.Alias != "" {
		return e.Alias
	}
	return guessImportLocalName(e.Path)
}

func (is *importSet) ensureFile(path string) *importChange {
	ic, ok := is.byFile[path]
	if !ok {
		ic = &importChange{}
		is.byFile[path] = ic
	}
	return ic
}

// importInfo describes a single needed import during collision resolution.
type importInfo struct {
	path      string
	localName string
	spec      *ast.ImportSpec
}

// resolveCollisions assigns aliases to imports that share the same localName.
// The first import (by sorted order) in each collision group keeps its
// localName; subsequent ones get a parentPrefixed alias or numeric suffix.
// usedNames is updated in place.
func resolveCollisions(infos []importInfo, usedNames map[string]bool) map[string]string {
	byLocal := make(map[string][]int)
	for i, info := range infos {
		byLocal[info.localName] = append(byLocal[info.localName], i)
	}

	aliases := make(map[string]string) // importPath -> alias
	for _, indices := range byLocal {
		if len(indices) < 2 {
			continue
		}
		// The first import keeps its short localName; only subsequent ones get aliased.
		for _, idx := range indices[1:] {
			info := infos[idx]
			alias := parentPrefixedName(info.path)
			if alias == info.localName || usedNames[alias] {
				base := alias
				for j := 2; usedNames[alias]; j++ {
					alias = base + strconv.Itoa(j)
				}
			}
			usedNames[alias] = true
			aliases[info.path] = alias
		}
	}
	return aliases
}

// findPkgForDir returns the package whose files reside in dir, or nil.
func findPkgForDir(ix *mast.Index, dir string) *mast.Package {
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if filepath.Dir(f.Path) == dir {
				return pkg
			}
		}
	}
	return nil
}

// importPath returns the unquoted import path from an ImportSpec.
func importPath(imp *ast.ImportSpec) string {
	p, _ := strconv.Unquote(imp.Path.Value)
	return p
}

// importLocalName returns the local name an import is known by.
func importLocalName(imp *ast.ImportSpec, impPath string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	return guessImportLocalName(impPath)
}

// packageLocalName returns the actual package name for a directory by
// checking the index. Falls back to guessImportLocalName when the
// package is not in the index (e.g., new target directories).
func packageLocalName(ix *mast.Index, dir string) string {
	realDir := evalDir(dir)
	for _, pkg := range ix.Pkgs {
		if len(pkg.Files) == 0 || strings.HasSuffix(pkg.Name, "_test") {
			continue
		}
		pkgDir := evalDir(filepath.Dir(pkg.Files[0].Path))
		if pkgDir == realDir {
			// Return the actual declared name, even "main". Using
			// "main" as the qualifier makes it obvious why the result
			// doesn't compile (main packages can't be imported), and
			// the warning from checkCrossPkgRefs already explains the
			// situation to the user.
			return pkg.Name
		}
	}
	return guessImportLocalName(guessImportPath(dir))
}

// evalDir resolves a directory path to a canonical form by applying
// filepath.Abs and filepath.EvalSymlinks (for macOS /var → /private/var).
func evalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return real
}

// guessImportLocalName derives the package name from an import path.
func guessImportLocalName(impPath string) string {
	base := path.Base(impPath)
	if len(base) >= 2 && base[0] == 'v' && base[1] >= '0' && base[1] <= '9' {
		parent := path.Base(path.Dir(impPath))
		if parent != "." && parent != "/" {
			base = parent
		}
	}
	base = strings.TrimPrefix(base, "go-")
	base = strings.TrimPrefix(base, "go_")
	base = strings.ReplaceAll(base, "-", "")
	base = strings.ReplaceAll(base, ".", "")
	if base == "" {
		return path.Base(impPath)
	}
	return base
}

// parentPrefixedName builds an alias by combining parent + base: "math/rand" -> "mathrand".
func parentPrefixedName(impPath string) string {
	base := path.Base(impPath)
	parent := path.Base(path.Dir(impPath))
	if parent == "." || parent == "/" || parent == "" {
		return base
	}
	parent = strings.ReplaceAll(parent, "-", "")
	parent = strings.ReplaceAll(parent, ".", "")
	parent = strings.ReplaceAll(parent, "_", "")
	return parent + base
}

// walkRange walks the AST and calls fn for nodes within the byte range [start, end).
func walkRange(file *ast.File, fset *token.FileSet, start, end int, fn func(ast.Node)) {
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		nStart := fset.Position(n.Pos()).Offset
		nEnd := fset.Position(n.End()).Offset
		if nEnd <= start || nStart >= end {
			return false
		}
		fn(n)
		return true
	})
}

// guessImportPath constructs an import path for a directory.
func guessImportPath(dir string) string {
	d := dir
	for {
		modPath := mast.ReadModulePath(d)
		if modPath != "" {
			rel, err := filepath.Rel(d, dir)
			if err == nil {
				if rel == "." {
					return modPath
				}
				return modPath + "/" + filepath.ToSlash(rel)
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return ""
}
