package mast_test

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loov/mast"
)

func loadTestdata(t *testing.T) *mast.Index {
	t.Helper()
	dir, err := filepath.Abs("testdata/example")
	if err != nil {
		t.Fatal(err)
	}
	ix, err := mast.Load(&mast.Config{Dir: dir}, "./...")
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
