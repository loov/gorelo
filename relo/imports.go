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
type importChange struct {
	Add     []importEntry     // imports to add
	Aliases map[string]string // importPath -> alias (from collision resolution)
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

	// Group relos by target file.
	byTarget := make(map[string][]*resolvedRelo)
	for _, rr := range resolved {
		byTarget[rr.TargetFile] = append(byTarget[rr.TargetFile], rr)
	}

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
				impPath, _ := strconv.Unquote(imp.Path.Value)
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
		if existingFile := findFileInIndex(ix, targetFile); existingFile != nil {
			for _, imp := range existingFile.Syntax.Imports {
				impPath, _ := strconv.Unquote(imp.Path.Value)
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

// findFileInIndex finds a mast.File by path in the index.
func findFileInIndex(ix *mast.Index, path string) *mast.File {
	return ix.FilesByPath[path]
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

// importLocalName returns the local name an import is known by.
func importLocalName(imp *ast.ImportSpec, impPath string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	return guessImportLocalName(impPath)
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
