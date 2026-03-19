package relo

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestReceiverTypeName(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "value receiver",
			src:  "package p\nfunc (t T) M() {}",
			want: "T",
		},
		{
			name: "pointer receiver",
			src:  "package p\nfunc (t *T) M() {}",
			want: "T",
		},
		{
			name: "nil recv",
			src:  "package p\nfunc F() {}",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, _ := parseSource(t, tt.src)
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				got := receiverTypeName(fd.Recv)
				if got != tt.want {
					t.Errorf("receiverTypeName = %q, want %q", got, tt.want)
				}
				return
			}
			t.Fatal("no func decl found")
		})
	}
}

func TestIsSamePackageDir(t *testing.T) {
	pkg := &mast.Package{
		Name: "pkg",
		Files: []*mast.File{
			{Path: "/home/user/project/pkg/foo.go"},
		},
	}

	tests := []struct {
		target string
		want   bool
	}{
		{"/home/user/project/pkg/bar.go", true},
		{"/home/user/project/pkg/sub/baz.go", false},
		{"/home/user/project/other/baz.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got := isSamePackageDir(pkg, tt.target)
			if got != tt.want {
				t.Errorf("isSamePackageDir(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestIsSamePackageDir_EmptyPackage(t *testing.T) {
	pkg := &mast.Package{Name: "pkg"}
	if isSamePackageDir(pkg, "/any/path.go") {
		t.Error("expected false for empty package")
	}
}

// TestResolve_RejectsUntrackedIdent tests that resolve returns an error
// for an ident not tracked by the index.
func TestResolve_RejectsUntrackedIdent(t *testing.T) {
	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X = 1\n",
	})

	// Create a fake ident not in the index.
	fakeIdent := &ast.Ident{Name: "NotInIndex"}
	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: fakeIdent}}, plan)
	if !errContains(err, "not tracked") {
		t.Fatalf("expected 'not tracked' error, got: %v", err)
	}
}

// TestResolve_RejectsFieldMove tests that fields cannot be moved.
func TestResolve_RejectsFieldMove(t *testing.T) {
	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct { F int }\n",
	})

	// Find the field ident.
	fieldIdent := findIdentInIndex(ix, "F")
	if fieldIdent == nil {
		t.Fatal("field F not found")
	}

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: fieldIdent, MoveTo: "/tmp/target.go"}}, plan)
	if !errContains(err, "cannot be moved") {
		t.Fatalf("expected 'cannot be moved' error, got: %v", err)
	}
}

// TestResolve_FieldRenameAllowed tests that fields can be renamed.
func TestResolve_FieldRenameAllowed(t *testing.T) {
	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct { F int }\n",
	})

	fieldIdent := findIdentInIndex(ix, "F")
	if fieldIdent == nil {
		t.Fatal("field F not found")
	}

	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{Ident: fieldIdent, Rename: "G"}}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved relo, got %d", len(resolved))
	}
	if resolved[0].TargetName != "G" {
		t.Errorf("expected target name G, got %q", resolved[0].TargetName)
	}
}

// TestResolve_DeduplicateByGroup tests that duplicate relos for the same
// group are deduplicated.
func TestResolve_DeduplicateByGroup(t *testing.T) {
	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X = 1\nfunc F() { _ = X }\n",
	})

	// Find the var X ident — there's a def and a use, both in the same group.
	varIdent := findDefIdentInIndex(ix, "X")
	if varIdent == nil {
		t.Fatal("var X not found")
	}

	plan := &Plan{}
	relos := []Relo{
		{Ident: varIdent, MoveTo: "/tmp/target.go"},
		{Ident: varIdent, MoveTo: "/tmp/target.go"},
	}
	resolved, err := resolve(ix, relos, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved relo after dedup, got %d", len(resolved))
	}
}

// TestResolve_ConstructorWarning tests that moving a type without its
// constructor generates a warning.
func TestResolve_ConstructorWarning(t *testing.T) {
	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype Foo struct{}\n\nfunc NewFoo() *Foo { return &Foo{} }\n",
	})

	typeIdent := findDefIdentInIndex(ix, "Foo")
	if typeIdent == nil {
		t.Fatal("type Foo not found")
	}

	pkgDir := dirOf(ix.Pkgs[0].Files[0].Path)
	plan := &Plan{}
	_, err := resolve(ix, []Relo{{
		Ident:  typeIdent,
		MoveTo: filepath.Join(pkgDir, "target.go"),
	}}, plan)
	if err != nil {
		t.Fatal(err)
	}

	if !hasWarning(plan, "constructor NewFoo") {
		t.Errorf("expected constructor warning, got: %v", plan.Warnings)
	}
}

// TestResolve_MethodAutoSynthesis tests that moving a type automatically
// adds its methods.
func TestResolve_MethodAutoSynthesis(t *testing.T) {
	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct{}\n\nfunc (t T) M() {}\n\nfunc (t *T) N() {}\n",
	})

	typeIdent := findDefIdentInIndex(ix, "T")
	if typeIdent == nil {
		t.Fatal("type T not found")
	}

	pkgDir := dirOf(ix.Pkgs[0].Files[0].Path)
	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{
		Ident:  typeIdent,
		MoveTo: filepath.Join(pkgDir, "target.go"),
	}}, plan)
	if err != nil {
		t.Fatal(err)
	}

	// Should have T + M + N = 3 resolved relos.
	if len(resolved) < 3 {
		names := make([]string, len(resolved))
		for i, rr := range resolved {
			names[i] = rr.Group.Name
		}
		t.Errorf("expected at least 3 resolved relos (T + methods), got %d: %v", len(resolved), names)
	}

	// Check that methods are marked as synthesized.
	synthCount := 0
	for _, rr := range resolved {
		if rr.Synthesized {
			synthCount++
		}
	}
	if synthCount != 2 {
		t.Errorf("expected 2 synthesized methods, got %d", synthCount)
	}
}

// loadTestIndex creates a temporary Go module from the given files,
// loads it with mast.Load, and returns the index.
func loadTestIndex(t *testing.T, files map[string]string) *mast.Index {
	t.Helper()

	dir := t.TempDir()

	// Write go.mod.
	modContent := "module example.com/testpkg\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Write source files.
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ix, err := mast.Load(&mast.Config{Dir: dir}, ".")
	if err != nil {
		t.Fatal("mast.Load:", err)
	}
	return ix
}

// findIdentInIndex finds any *ast.Ident with the given name that is tracked by the index.
func findIdentInIndex(ix *mast.Index, name string) *ast.Ident {
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			var found *ast.Ident
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				if found != nil {
					return false
				}
				id, ok := n.(*ast.Ident)
				if !ok || id.Name != name {
					return true
				}
				if ix.Group(id) != nil {
					found = id
					return false
				}
				return true
			})
			if found != nil {
				return found
			}
		}
	}
	return nil
}

// findDefIdentInIndex finds a definition *ast.Ident by name.
func findDefIdentInIndex(ix *mast.Index, name string) *ast.Ident {
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Syntax.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Name.Name == name {
						return d.Name
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.Name == name {
								return s.Name
							}
						case *ast.ValueSpec:
							for _, n := range s.Names {
								if n.Name == name {
									return n
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}
