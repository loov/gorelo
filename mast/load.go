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
	src      []byte
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
	loadedDirs := map[string]bool{}
	for _, pkg := range initial {
		if pkg.PkgPath == "" {
			continue
		}

		if d := packageDir(pkg); d != "" {
			loadedDirs[d] = true
		}
		pkgs, errs := loadPackage(ix, pkg, pkgsCfg, depPkgs)
		ix.Pkgs = append(ix.Pkgs, pkgs...)
		ix.Errors = append(ix.Errors, errs...)
	}

	// Discover packages missed by packages.Load due to build constraints.
	// When all .go files in a directory have non-matching build constraints
	// (e.g., //go:build linux files on Windows), packages.Load skips the
	// directory entirely. Walk the filesystem to find these directories and
	// load them manually.
	if cfg.Dir != "" {
		var modPath, modDir string
		for _, pkg := range initial {
			if pkg.Module != nil && pkg.Module.Dir != "" {
				modPath = pkg.Module.Path
				modDir = pkg.Module.Dir
				break
			}
		}
		if modDir == "" {
			modPath, modDir = readModulePath(cfg.Dir)
		}

		if modDir != "" {
			missed := discoverMissedDirs(cfg.Dir, loadedDirs)
			for _, dir := range missed {
				rel, err := filepath.Rel(modDir, dir)
				if err != nil {
					continue
				}
				pkgPath := modPath
				if rel != "." {
					pkgPath = modPath + "/" + filepath.ToSlash(rel)
				}

				name := discoverPkgName(dir)
				if name == "" {
					continue
				}

				synth := &packages.Package{
					PkgPath: pkgPath,
					Name:    name,
					GoFiles: []string{filepath.Join(dir, "_.go")},
				}

				pkgs, errs := loadPackage(ix, synth, pkgsCfg, depPkgs)
				ix.Pkgs = append(ix.Pkgs, pkgs...)
				ix.Errors = append(ix.Errors, errs...)
			}
		}
	}

	// Resolve idents that were untracked due to build-tag partitioning.
	// When conflicting build constraints cause separate type-check passes,
	// cross-partition references (e.g. a const defined in one build tag
	// and used in another) are unresolved. Link them to matching
	// package-level groups by name.
	resolveUntracked(ix)

	ix.FilesByPath = make(map[string]*File, len(ix.Pkgs)*4)
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			ix.FilesByPath[f.Path] = f
		}
	}

	return ix, nil
}

// loadPackage loads a single target package: discovers all files,
// parses them, partitions by build constraints, and type-checks each set.
// Test files (_test.go) are included. Files declaring the external test
// package (e.g. package foo_test) are returned as a separate Package.
func loadPackage(ix *Index, pkg *packages.Package, cfg *packages.Config, depPkgs map[string]*types.Package) ([]*Package, []error) {
	dir := packageDir(pkg)
	if dir == "" {
		return nil, []error{fmt.Errorf("cannot determine directory for package %s", pkg.PkgPath)}
	}

	// Discover all .go files in the directory.
	allPaths, err := discoverGoFiles(dir)
	if err != nil {
		return nil, []error{err}
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
			src:      src,
			buildTag: tag,
		})
	}

	// Collect imports from all parsed files and load any missing dependencies.
	// This handles cases where build-constrained files import packages that
	// weren't discovered during the initial packages.Load (e.g., a file with
	// //go:build linux importing "example/linux" won't be seen on Windows).
	var missingImports []string
	for _, pf := range parsed {
		for _, imp := range pf.syntax.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if _, ok := depPkgs[path]; !ok {
				missingImports = append(missingImports, path)
			}
		}
	}
	if len(missingImports) > 0 {
		extraPkgs, loadErr := packages.Load(cfg, missingImports...)
		if loadErr == nil {
			var collectDeps func(p *packages.Package)
			collectDeps = func(p *packages.Package) {
				for _, imp := range p.Imports {
					if _, ok := depPkgs[imp.PkgPath]; ok {
						continue
					}
					if imp.Types != nil {
						depPkgs[imp.PkgPath] = imp.Types
					}
					collectDeps(imp)
				}
			}
			for _, ep := range extraPkgs {
				if ep.PkgPath != "" && ep.Types != nil {
					depPkgs[ep.PkgPath] = ep.Types
				}
				collectDeps(ep)
			}
		}
	}

	// Create packages and assign files.
	mpkg := &Package{
		Name: pkg.Name,
		Path: pkg.PkgPath,
	}
	extTestName := pkg.Name + "_test"
	var extTestPkg *Package

	fileMap := map[*ast.File]*File{}
	var mainFiles, extTestFiles []parsedFile
	for _, pf := range parsed {
		isExtTest := pf.syntax.Name.Name == extTestName
		owner := mpkg
		if isExtTest {
			if extTestPkg == nil {
				extTestPkg = &Package{
					Name: extTestName,
					Path: pkg.PkgPath + "_test",
				}
			}
			owner = extTestPkg
		}

		mf := &File{
			Path:     pf.path,
			Pkg:      owner,
			Syntax:   pf.syntax,
			Src:      pf.src,
			BuildTag: pf.buildTag,
		}
		owner.Files = append(owner.Files, mf)
		fileMap[pf.syntax] = mf

		if isExtTest {
			extTestFiles = append(extTestFiles, pf)
		} else {
			mainFiles = append(mainFiles, pf)
		}
	}

	// Type-check main package files (including same-package _test.go files).
	typeCheckFiles(ix, mainFiles, fileMap, pkg.PkgPath, pkg.Name, depPkgs)

	// Type-check external test package files.
	if len(extTestFiles) > 0 {
		typeCheckFiles(ix, extTestFiles, fileMap, pkg.PkgPath+"_test", extTestName, depPkgs)
	}

	pkgs := []*Package{mpkg}
	if extTestPkg != nil {
		pkgs = append(pkgs, extTestPkg)
	}
	return pkgs, errs
}

// typeCheckFiles partitions files by build constraints and type-checks
// each partition under the given package path and name.
func typeCheckFiles(ix *Index, files []parsedFile, fileMap map[*ast.File]*File, pkgPath, pkgName string, depPkgs map[string]*types.Package) {
	sets := partitionFiles(files)

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
	for line := range strings.SplitSeq(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if after, ok := strings.CutPrefix(line, "//go:build "); ok {
			return after
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") {
			continue
		}
		break
	}

	// Check filename-based constraints (e.g., *_linux.go, *_linux_amd64.go).
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, ".go")
	name = strings.TrimSuffix(name, "_test")
	parts := strings.Split(name, "_")
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		// *_GOOS_GOARCH.go: both second-to-last (GOOS) and last (GOARCH).
		if len(parts) >= 3 && isKnownGOARCH(last) {
			secondLast := parts[len(parts)-2]
			if isKnownGOOS(secondLast) {
				return secondLast + " && " + last
			}
			return last
		}
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

	for i := range tagOrder {
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

// discoverMissedDirs walks dir recursively and returns directories containing
// .go files that are not in loadedDirs. It skips directories starting with
// "." or "_", and skips "vendor" and "testdata" directories, matching the
// behavior of Go's "./..." pattern.
func discoverMissedDirs(dir string, loadedDirs map[string]bool) []string {
	var missed []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != dir {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
		}
		if loadedDirs[path] {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				missed = append(missed, path)
				return nil
			}
		}
		return nil
	})
	return missed
}

// discoverPkgName parses the package clause from the first non-test .go file
// in dir to determine the package name.
func discoverPkgName(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err != nil || f.Name == nil {
			continue
		}
		return f.Name.Name
	}
	return ""
}

// ReadModulePath reads the module path from the go.mod file in dir.
// Returns "" if no go.mod exists or the module line cannot be parsed.
func ReadModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

// readModulePath reads the module path and directory from the go.mod file
// in dir or any parent directory.
func readModulePath(dir string) (modPath, modDir string) {
	d := dir
	for {
		if mod := ReadModulePath(d); mod != "" {
			return mod, d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "", ""
}
