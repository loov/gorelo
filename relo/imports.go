package relo

import (
	"go/ast"
	"go/token"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/loov/gorelo/mast"
)

// importChange describes import modifications needed for a file.
// Existing is loaded lazily from the file's parsed AST on the first
// call to addImportEntry; Add accumulates new imports. Each entry's
// Alias field carries any explicit alias (from the source spec, the
// package's real name when it differs from the path basename, or
// collision resolution).
type importChange struct {
	Existing       []importEntry
	existingLoaded bool
	Add            []importEntry

	// used maps local name → import path for all entries in Existing and Add.
	// Maintained incrementally by loadExistingImports and addImportEntry.
	used map[string]string
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

// warnNontransferableImports emits a warning for every dot or blank
// import in any source file with a moved declaration. These imports
// cannot be auto-transferred to the destination — the user must
// reproduce them manually if the moved code relies on them.
func warnNontransferableImports(ix *mast.Index, resolved []*resolvedRelo, plan *Plan) {
	for _, rr := range resolved {
		if rr.File == nil {
			continue
		}
		for _, imp := range rr.File.Syntax.Imports {
			impPath := importPath(imp)
			localName := importLocalName(imp, impPath)
			switch localName {
			case ".":
				plan.Warnings.AddAtf(rr, ix,
					"moved decl %s uses dot import %s which cannot be automatically transferred",
					rr.Group.Name, imp.Path.Value)
			case "_":
				plan.Warnings.AddAtf(rr, ix,
					"moved decl %s has blank import %s for side effects which cannot be automatically transferred",
					rr.Group.Name, imp.Path.Value)
			}
		}
	}
}

// addImportEntry registers entry as an import to add to filePath. On the
// first call for a file, the file's pre-existing imports are loaded
// from the index into ic.Existing. When entry.Alias is empty and the
// package's real name (per ix.Pkgs) differs from the path basename, an
// explicit alias is set so the rewritten code's qualifier resolves.
// The entry is deduplicated against both Existing and Add by import
// path; if its desired local name still collides with a different
// already-known import, a parent-prefixed alias is assigned and
// recorded in ic.Aliases. applyImportsPass picks the entries up after
// assembly.
func addImportEntry(is *importSet, ix *mast.Index, filePath string, entry importEntry) {
	ic := is.ensureFile(filePath)
	loadExistingImports(ic, ix, filePath)

	if entry.Alias == "" {
		if real := packageNameForImport(ix, entry.Path); real != guessImportLocalName(entry.Path) {
			entry.Alias = real
		}
	}

	desired := importEntryLocalName(entry)
	if owner, exists := ic.used[desired]; exists {
		if owner == entry.Path {
			return // already imported (existing or queued)
		}
		alias := parentPrefixedName(entry.Path)
		if alias == "" || alias == desired {
			alias = desired
		}
		if _, taken := ic.used[alias]; taken {
			base := alias
			for i := 2; ; i++ {
				alias = base + strconv.Itoa(i)
				if _, taken := ic.used[alias]; !taken {
					break
				}
			}
		}
		entry.Alias = alias
	}
	ic.Add = append(ic.Add, entry)
	ic.used[importEntryLocalName(entry)] = entry.Path
}

// packageNameForImport returns the actual package name for impPath if
// the package is in ix; otherwise the guess from the path's basename.
func packageNameForImport(ix *mast.Index, impPath string) string {
	if ix != nil {
		for _, pkg := range ix.Pkgs {
			if pkg.Path == impPath {
				return pkg.Name
			}
		}
	}
	return guessImportLocalName(impPath)
}

// loadExistingImports populates ic.Existing from the file's parsed
// imports (once per file).
func loadExistingImports(ic *importChange, ix *mast.Index, filePath string) {
	if ic.existingLoaded {
		return
	}
	ic.existingLoaded = true
	if ic.used == nil {
		ic.used = make(map[string]string)
	}
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
		ic.used[importEntryLocalName(ie)] = impPath
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

// findPkgForDir returns the non-test package whose files reside in dir, or nil.
func findPkgForDir(ix *mast.Index, dir string) *mast.Package {
	for _, pkg := range ix.Pkgs {
		if len(pkg.Files) == 0 || strings.HasSuffix(pkg.Name, "_test") {
			continue
		}
		if filepath.Dir(pkg.Files[0].Path) == dir {
			return pkg
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
	if pkg := findPkgForDir(ix, dir); pkg != nil {
		return pkg.Name
	}
	return guessImportLocalName(guessImportPath(dir))
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
