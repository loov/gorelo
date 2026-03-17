package mast

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// parsedFile holds a parsed Go source file with its metadata.
type parsedFile struct {
	path     string
	syntax   *ast.File
	buildTag string
}

// load implements the hybrid loading strategy.
func load(cfg *Config, patterns ...string) (*Index, error) {
	ix := &Index{
		Fset:        token.NewFileSet(),
		groups:      map[*ast.Ident]*Group{},
		groupsByKey: map[objectKey]*Group{},
	}

	// Step 1: Load packages via go/packages to get dependency type info.
	pkgsCfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedTypes | packages.NeedModule,
		Dir:  cfg.Dir,
		Env:  cfg.Env,
		Fset: ix.Fset,
	}

	initial, err := packages.Load(pkgsCfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("packages.Load: %w", err)
	}

	// Collect all dependency packages for the importer.
	depPkgs := map[string]*types.Package{}
	var collectDeps func(pkg *packages.Package)
	collectDeps = func(pkg *packages.Package) {
		for _, imp := range pkg.Imports {
			if _, ok := depPkgs[imp.PkgPath]; ok {
				continue
			}
			if imp.Types != nil {
				depPkgs[imp.PkgPath] = imp.Types
			}
			collectDeps(imp)
		}
	}
	for _, pkg := range initial {
		collectDeps(pkg)
	}

	// Register all initial packages' types before processing,
	// so cross-package references resolve correctly regardless of order.
	for _, pkg := range initial {
		if pkg.PkgPath != "" && pkg.Types != nil {
			depPkgs[pkg.PkgPath] = pkg.Types
		}
	}

	// Step 2-5: For each target package, discover all files, parse, partition, type-check.
	for _, pkg := range initial {
		if pkg.PkgPath == "" {
			continue
		}

		mpkg, errs := loadPackage(ix, pkg, depPkgs)
		if mpkg != nil {
			ix.Pkgs = append(ix.Pkgs, mpkg)
		}
		ix.Errors = append(ix.Errors, errs...)
	}

	return ix, nil
}

// loadPackage loads a single target package: discovers all files,
// parses them, partitions by build constraints, and type-checks each set.
// Test files (_test.go) are included. Files declaring the external test
// package (e.g. package foo_test) are type-checked after the main
// package so they can import it.
func loadPackage(ix *Index, pkg *packages.Package, depPkgs map[string]*types.Package) (*Package, []error) {
	mpkg := &Package{
		Name: pkg.Name,
		Path: pkg.PkgPath,
	}

	dir := packageDir(pkg)
	if dir == "" {
		return mpkg, []error{fmt.Errorf("cannot determine directory for package %s", pkg.PkgPath)}
	}

	// Discover all .go files in the directory.
	allPaths, err := discoverGoFiles(dir)
	if err != nil {
		return mpkg, []error{err}
	}

	// Parse all files.
	var parsed []parsedFile
	var errs []error

	for _, path := range allPaths {
		src, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		tag := extractBuildTag(path, src)

		f, parseErr := parser.ParseFile(ix.Fset, path, src, parser.ParseComments)
		if parseErr != nil {
			errs = append(errs, parseErr)
			if f == nil {
				continue
			}
		}

		parsed = append(parsed, parsedFile{
			path:     path,
			syntax:   f,
			buildTag: tag,
		})
	}

	// Build File objects.
	fileMap := map[*ast.File]*File{}
	for _, pf := range parsed {
		mf := &File{
			Path:     pf.path,
			Syntax:   pf.syntax,
			BuildTag: pf.buildTag,
		}
		mpkg.Files = append(mpkg.Files, mf)
		fileMap[pf.syntax] = mf
	}

	// Split files by declared package name: same-package test files
	// (package foo) are type-checked with the main files; external test
	// files (package foo_test) need a separate pass.
	extTestName := pkg.Name + "_test"
	var mainFiles, extTestFiles []parsedFile
	for _, pf := range parsed {
		if pf.syntax.Name.Name == extTestName {
			extTestFiles = append(extTestFiles, pf)
		} else {
			mainFiles = append(mainFiles, pf)
		}
	}

	// Type-check main package files (including same-package _test.go files).
	mainErrs := typeCheckFiles(ix, mainFiles, fileMap, pkg.PkgPath, pkg.Name, depPkgs)
	errs = append(errs, mainErrs...)

	// Type-check external test package files. They can import the main
	// package, so register its types first.
	if len(extTestFiles) > 0 {
		extErrs := typeCheckFiles(ix, extTestFiles, fileMap, pkg.PkgPath+"_test", extTestName, depPkgs)
		errs = append(errs, extErrs...)
	}

	return mpkg, errs
}

// typeCheckFiles partitions files by build constraints and type-checks
// each partition under the given package path and name.
func typeCheckFiles(ix *Index, files []parsedFile, fileMap map[*ast.File]*File, pkgPath, pkgName string, depPkgs map[string]*types.Package) []error {
	sets := partitionFiles(files)
	var errs []error

	for _, set := range sets {
		astFiles := make([]*ast.File, len(set))
		for i, pf := range set {
			astFiles[i] = pf.syntax
		}

		info := &types.Info{
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}

		tpkg := types.NewPackage(pkgPath, pkgName)
		tcfg := &types.Config{
			Importer: importerFunc(func(path string) (*types.Package, error) {
				if tp, ok := depPkgs[path]; ok {
					return tp, nil
				}
				return nil, fmt.Errorf("package %s not found in dependencies", path)
			}),
			Error: func(error) {}, // swallow errors
		}

		_ = types.NewChecker(tcfg, ix.Fset, tpkg, info).Files(astFiles)

		resolveInfo(ix, info, fileMap)
	}

	return errs
}

// importerFunc adapts a function to the types.Importer interface.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) {
	return f(path)
}

// packageDir returns the directory for a package.
func packageDir(pkg *packages.Package) string {
	for _, files := range [][]string{pkg.GoFiles, pkg.OtherFiles, pkg.CompiledGoFiles} {
		if len(files) > 0 {
			return filepath.Dir(files[0])
		}
	}
	if pkg.Module != nil && pkg.Module.Dir != "" {
		rel := strings.TrimPrefix(pkg.PkgPath, pkg.Module.Path)
		return filepath.Join(pkg.Module.Dir, filepath.FromSlash(rel))
	}
	return ""
}

// discoverGoFiles returns all .go files in dir, including test files.
func discoverGoFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files, nil
}

// extractBuildTag returns the build constraint string from a file, or "".
// It checks both //go:build directives and filename-based constraints.
func extractBuildTag(path string, src []byte) string {
	// Check for //go:build directive.
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "//go:build ") {
			return strings.TrimPrefix(line, "//go:build ")
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") {
			continue
		}
		break
	}

	// Check filename-based constraints (e.g., *_linux.go).
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, ".go")
	parts := strings.Split(name, "_")
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		if isKnownGOOS(last) || isKnownGOARCH(last) {
			return last
		}
	}

	return ""
}

// partitionFiles groups files into sets that can be type-checked together.
func partitionFiles(files []parsedFile) [][]parsedFile {
	var unconstrained []parsedFile
	tagGroups := map[string][]parsedFile{}
	var tagOrder []string

	for _, f := range files {
		if f.buildTag == "" {
			unconstrained = append(unconstrained, f)
		} else {
			if _, exists := tagGroups[f.buildTag]; !exists {
				tagOrder = append(tagOrder, f.buildTag)
			}
			tagGroups[f.buildTag] = append(tagGroups[f.buildTag], f)
		}
	}

	if len(tagGroups) == 0 {
		if len(unconstrained) > 0 {
			return [][]parsedFile{unconstrained}
		}
		return nil
	}

	// Check if constrained files conflict (define same top-level names).
	if hasConflictingDefinitions(tagGroups, tagOrder) {
		// One pass per tag group, each including unconstrained files.
		sets := make([][]parsedFile, 0, len(tagOrder))
		for _, tag := range tagOrder {
			set := make([]parsedFile, 0, len(unconstrained)+len(tagGroups[tag]))
			set = append(set, unconstrained...)
			set = append(set, tagGroups[tag]...)
			sets = append(sets, set)
		}
		return sets
	}

	// No conflicts — all files together.
	all := make([]parsedFile, 0, len(files))
	all = append(all, unconstrained...)
	for _, tag := range tagOrder {
		all = append(all, tagGroups[tag]...)
	}
	return [][]parsedFile{all}
}

// hasConflictingDefinitions checks if different tag groups define the same
// top-level names.
func hasConflictingDefinitions(tagGroups map[string][]parsedFile, tagOrder []string) bool {
	type nameSet = map[string]bool
	groupNames := map[string]nameSet{}

	for _, tag := range tagOrder {
		names := nameSet{}
		for _, f := range tagGroups[tag] {
			for _, decl := range f.syntax.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil {
						names[d.Name.Name] = true
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							names[s.Name.Name] = true
						case *ast.ValueSpec:
							for _, n := range s.Names {
								names[n.Name] = true
							}
						}
					}
				}
			}
		}
		groupNames[tag] = names
	}

	for i := 0; i < len(tagOrder); i++ {
		for j := i + 1; j < len(tagOrder); j++ {
			a, b := groupNames[tagOrder[i]], groupNames[tagOrder[j]]
			for name := range a {
				if b[name] {
					return true
				}
			}
		}
	}
	return false
}

var knownGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
}

var knownGOARCH = map[string]bool{
	"386": true, "amd64": true, "arm": true, "arm64": true,
	"loong64": true, "mips": true, "mips64": true, "mips64le": true,
	"mipsle": true, "ppc64": true, "ppc64le": true, "riscv64": true,
	"s390x": true, "wasm": true,
}

func isKnownGOOS(s string) bool   { return knownGOOS[s] }
func isKnownGOARCH(s string) bool { return knownGOARCH[s] }
