package mast_test

import (
	"go/ast"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/loov/gorelo/mast"
)

var (
	cachedIndex    = map[string]*mast.Index{}
	cachedIndexErr = map[string]error{}
	cachedIndexMu  sync.Mutex
)

func loadTestdata(t *testing.T) *mast.Index {
	t.Helper()
	return loadTestdataWith(t, "./...")
}

// loadTestdataRoot loads only the root example package (not sub-packages).
// This exercises the dependency loading path for build-constrained files
// that import sub-packages not present in the initial packages.Load result.
func loadTestdataRoot(t *testing.T) *mast.Index {
	t.Helper()
	return loadTestdataWith(t, ".")
}

func loadTestdataWith(t *testing.T, pattern string) *mast.Index {
	t.Helper()

	cachedIndexMu.Lock()
	defer cachedIndexMu.Unlock()

	if ix, ok := cachedIndex[pattern]; ok {
		if err := cachedIndexErr[pattern]; err != nil {
			t.Fatal(err)
		}
		return ix
	}

	dir, err := filepath.Abs("testdata/example")
	if err != nil {
		t.Fatal(err)
	}
	ix, err := mast.Load(&mast.Config{Dir: dir}, pattern)
	cachedIndex[pattern] = ix
	cachedIndexErr[pattern] = err
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// findIdents returns all *ast.Ident with the given name across all packages/files.
func findIdents(ix *mast.Index, name string) []*ast.Ident {
	var result []*ast.Ident
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == name {
					result = append(result, id)
				}
				return true
			})
		}
	}
	return result
}

// findIdentsInFile returns all *ast.Ident with name in files whose path contains pathFragment.
func findIdentsInFile(ix *mast.Index, name, pathFragment string) []*ast.Ident {
	var result []*ast.Ident
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			if !strings.Contains(file.Path, pathFragment) {
				continue
			}
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == name {
					result = append(result, id)
				}
				return true
			})
		}
	}
	return result
}

// findIdentsInFunc returns all *ast.Ident with the given name that appear
// inside the function (or method) named funcName in the given file.
func findIdentsInFunc(ix *mast.Index, identName, pathFragment, funcName string) []*ast.Ident {
	var result []*ast.Ident
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			if !strings.Contains(file.Path, pathFragment) {
				continue
			}
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name.Name != funcName {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && id.Name == identName {
						result = append(result, id)
					}
					return true
				})
			}
		}
	}
	return result
}

// findFieldGroup finds the Group for a field named fieldName owned by the struct
// defined in the given file. Returns nil if not found.
func findFieldGroup(ix *mast.Index, fieldName, pathFragment string) *mast.Group {
	for _, id := range findIdentsInFile(ix, fieldName, pathFragment) {
		g := ix.Group(id)
		if g != nil && g.Kind == mast.Field {
			return g
		}
	}
	return nil
}

// findTypeGroup finds the Group for a type named typeName defined in the given file.
func findTypeGroup(ix *mast.Index, typeName, pathFragment string) *mast.Group {
	for _, id := range findIdentsInFile(ix, typeName, pathFragment) {
		g := ix.Group(id)
		if g != nil && g.Kind == mast.TypeName {
			return g
		}
	}
	return nil
}

// findMethodGroup finds the Group for a method named methodName defined in the given file.
func findMethodGroup(ix *mast.Index, methodName, pathFragment string) *mast.Group {
	for _, id := range findIdentsInFile(ix, methodName, pathFragment) {
		g := ix.Group(id)
		if g != nil && g.Kind == mast.Method {
			return g
		}
	}
	return nil
}

// findFuncGroup finds the Group for a function named funcName defined in the given file.
func findFuncGroup(ix *mast.Index, funcName, pathFragment string) *mast.Group {
	for _, id := range findIdentsInFile(ix, funcName, pathFragment) {
		g := ix.Group(id)
		if g != nil && g.Kind == mast.Func {
			return g
		}
	}
	return nil
}

func TestLoad(t *testing.T) {
	ix := loadTestdata(t)

	if len(ix.Pkgs) == 0 {
		t.Fatal("expected at least one package")
	}

	var found bool
	for _, pkg := range ix.Pkgs {
		if pkg.Path == "example" {
			found = true
			hasLinux := false
			hasWindows := false
			for _, f := range pkg.Files {
				if strings.Contains(f.Path, "platform_linux.go") {
					hasLinux = true
				}
				if strings.Contains(f.Path, "platform_windows.go") {
					hasWindows = true
				}
			}
			if !hasLinux {
				t.Error("missing platform_linux.go")
			}
			if !hasWindows {
				t.Error("missing platform_windows.go")
			}
		}
	}
	if !found {
		t.Error("example package not found")
	}
}

func TestAllPackagesLoaded(t *testing.T) {
	ix := loadTestdata(t)

	paths := map[string]bool{}
	for _, pkg := range ix.Pkgs {
		paths[pkg.Path] = true
	}

	for _, want := range []string{"example", "example/linux", "example/windows"} {
		if !paths[want] {
			t.Errorf("package %s not loaded", want)
		}
	}
}

func TestTestFilesLoaded(t *testing.T) {
	ix := loadTestdata(t)

	var hasSamePkg, hasExtPkg bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "example_test.go") {
				hasSamePkg = true
				if pkg.Path != "example" {
					t.Errorf("example_test.go should be in package example, got %s", pkg.Path)
				}
			}
			if strings.Contains(f.Path, "example_ext_test.go") {
				hasExtPkg = true
				if pkg.Path != "example_test" {
					t.Errorf("example_ext_test.go should be in package example_test, got %s", pkg.Path)
				}
			}
		}
	}
	if !hasSamePkg {
		t.Error("same-package test file example_test.go not loaded")
	}
	if !hasExtPkg {
		t.Error("external test file example_ext_test.go not loaded")
	}
}

func TestSamePackageTestIdents(t *testing.T) {
	ix := loadTestdata(t)

	// Server is used in example_test.go (same-package test).
	// It should be in the same group as the Server defined in structs.go.
	serverInTest := findIdentsInFile(ix, "Server", "example_test.go")
	if len(serverInTest) == 0 {
		t.Fatal("Server not found in example_test.go")
	}

	grp := ix.Group(serverInTest[0])
	if grp == nil {
		t.Fatal("Server ident in example_test.go has no group")
	}

	// The group should also contain idents from structs.go.
	var hasStructs bool
	for _, id := range grp.Idents {
		if strings.Contains(id.File.Path, "structs.go") {
			hasStructs = true
			break
		}
	}
	if !hasStructs {
		t.Error("Server group does not include idents from structs.go")
	}
}

func TestExternalTestIdents(t *testing.T) {
	ix := loadTestdata(t)

	// Counter is used in example_ext_test.go (external test package).
	counterInTest := findIdentsInFile(ix, "Counter", "example_ext_test.go")
	if len(counterInTest) == 0 {
		t.Fatal("Counter not found in example_ext_test.go")
	}

	grp := ix.Group(counterInTest[0])
	if grp == nil {
		t.Fatal("Counter ident in example_ext_test.go has no group")
	}

	// The group should also contain idents from types.go.
	var hasTypes bool
	for _, id := range grp.Idents {
		if strings.Contains(id.File.Path, "types.go") {
			hasTypes = true
			break
		}
	}
	if !hasTypes {
		t.Error("Counter group does not include idents from types.go")
	}
}

func TestFilePkg(t *testing.T) {
	ix := loadTestdata(t)

	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if f.Pkg != pkg {
				t.Errorf("file %s: Pkg points to %q, expected %q", f.Path, f.Pkg.Path, pkg.Path)
			}
		}
	}
}

func TestIdentPkg(t *testing.T) {
	ix := loadTestdata(t)

	// Server defined in structs.go should have File.Pkg.Path == "example".
	serverIdents := findIdentsInFile(ix, "Server", "structs.go")
	if len(serverIdents) == 0 {
		t.Fatal("Server not found in structs.go")
	}
	grp := ix.Group(serverIdents[0])
	for _, id := range grp.Idents {
		if id.File.Pkg == nil {
			t.Errorf("ident %s in %s has nil File.Pkg", id.Ident.Name, id.File.Path)
		}
	}

	// Counter used in example_ext_test.go should have File.Pkg.Path == "example_test".
	counterInExt := findIdentsInFile(ix, "Counter", "example_ext_test.go")
	if len(counterInExt) == 0 {
		t.Fatal("Counter not found in example_ext_test.go")
	}
	extGrp := ix.Group(counterInExt[0])
	for _, id := range extGrp.Idents {
		if strings.Contains(id.File.Path, "example_ext_test.go") {
			if id.File.Pkg.Path != "example_test" {
				t.Errorf("Counter ident in ext test: File.Pkg.Path = %q, want example_test", id.File.Pkg.Path)
			}
		}
		if strings.Contains(id.File.Path, "types.go") {
			if id.File.Pkg.Path != "example" {
				t.Errorf("Counter ident in types.go: File.Pkg.Path = %q, want example", id.File.Pkg.Path)
			}
		}
	}
}

func TestQualifier(t *testing.T) {
	ix := loadTestdata(t)

	// In example_ext_test.go, "example.Counter" is a qualified reference.
	// The Counter ident should have Qualifier pointing to the "example" ident.
	counterInExt := findIdentsInFile(ix, "Counter", "example_ext_test.go")
	if len(counterInExt) == 0 {
		t.Fatal("Counter not found in example_ext_test.go")
	}
	grp := ix.Group(counterInExt[0])
	if grp == nil {
		t.Fatal("Counter has no group")
	}

	var qualifiedCount, unqualifiedCount int
	for _, id := range grp.Idents {
		if id.Qualifier != nil {
			qualifiedCount++
			if id.Qualifier.Name != "example" {
				t.Errorf("Counter qualifier = %q, want %q", id.Qualifier.Name, "example")
			}
		} else {
			unqualifiedCount++
		}
	}
	if qualifiedCount == 0 {
		t.Error("expected at least one qualified Counter reference")
	}
	if unqualifiedCount == 0 {
		t.Error("expected at least one unqualified Counter reference (definition)")
	}
}

func TestQualifierAbsentForLocal(t *testing.T) {
	ix := loadTestdata(t)

	// Server is used in structs.go without a package qualifier.
	serverIdents := findIdentsInFile(ix, "Server", "structs.go")
	if len(serverIdents) == 0 {
		t.Fatal("Server not found in structs.go")
	}
	grp := ix.Group(serverIdents[0])
	for _, id := range grp.Idents {
		if !strings.Contains(id.File.Path, "structs.go") && !strings.Contains(id.File.Path, "example_test.go") {
			continue
		}
		if id.Qualifier != nil {
			t.Errorf("Server ident in %s has unexpected qualifier %q", id.File.Path, id.Qualifier.Name)
		}
	}
}

func TestEmptyFile(t *testing.T) {
	ix := loadTestdata(t)

	var found bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "empty.go") {
				found = true
			}
		}
	}
	if !found {
		t.Error("empty.go not loaded")
	}
}

func TestGroupDeduplication(t *testing.T) {
	ix := loadTestdata(t)

	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				grp := ix.Group(id)
				if grp == nil {
					return true
				}
				count := 0
				for _, gid := range grp.Idents {
					if gid.Ident == id {
						count++
					}
				}
				if count > 1 {
					t.Errorf("ident %s at %v appears %d times in its group", id.Name, id.Pos(), count)
				}
				return true
			})
		}
	}
}
