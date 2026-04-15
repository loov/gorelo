package relo

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestReceiverTypeName(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			file, _ := parseSource(t, tt.src)
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				got := mast.ReceiverTypeName(fd.Recv)
				if got != tt.want {
					t.Errorf("ReceiverTypeName = %q, want %q", got, tt.want)
				}
				return
			}
			t.Fatal("no func decl found")
		})
	}
}

func TestIsSamePackageDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	pkgDir := filepath.Join(base, "project", "pkg")

	pkg := &mast.Package{
		Name: "pkg",
		Files: []*mast.File{
			{Path: filepath.Join(pkgDir, "foo.go")},
		},
	}

	tests := []struct {
		target string
		want   bool
	}{
		{filepath.Join(pkgDir, "bar.go"), true},
		{filepath.Join(pkgDir, "sub", "baz.go"), false},
		{filepath.Join(base, "project", "other", "baz.go"), false},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			t.Parallel()

			got := isSamePackageDir(pkg, tt.target)
			if got != tt.want {
				t.Errorf("isSamePackageDir(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestIsSamePackageDir_RelativeTarget(t *testing.T) {
	t.Parallel()

	// Simulate the real scenario: package files have absolute paths
	// (from mast.Load), but the target comes from a rules file as a
	// relative path like "render_assets.go".
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	pkg := &mast.Package{
		Name: "pkg",
		Files: []*mast.File{
			{Path: filepath.Join(cwd, "existing.go")},
		},
	}

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{"bare filename", "new_file.go", true},
		{"dot-slash prefix", "./new_file.go", true},
		{"subdirectory", "sub/new_file.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := isSamePackageDir(pkg, tt.target)
			if got != tt.want {
				t.Errorf("isSamePackageDir(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestIsSamePackageDir_EmptyPackage(t *testing.T) {
	t.Parallel()

	pkg := &mast.Package{Name: "pkg"}
	if isSamePackageDir(pkg, "/any/path.go") {
		t.Error("expected false for empty package")
	}
}

// TestResolve_RejectsUntrackedIdent tests that resolve returns an error
// for an ident not tracked by the index.
func TestResolve_RejectsUntrackedIdent(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X = 1\n",
	})

	// Create a fake ident not in the index.
	fakeIdent := &ast.Ident{Name: "NotInIndex"}
	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: fakeIdent}}, nil, plan)
	if !errContains(err, "not tracked") {
		t.Fatalf("expected 'not tracked' error, got: %v", err)
	}
}

// TestResolve_RejectsFieldMove tests that fields cannot be moved.
func TestResolve_RejectsFieldMove(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct { F int }\n",
	})

	// Find the field ident.
	fieldIdent := findIdentInIndex(ix, "F")
	if fieldIdent == nil {
		t.Fatal("field F not found")
	}

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: fieldIdent, MoveTo: "/tmp/target.go"}}, nil, plan)
	if !errContains(err, "cannot be moved") {
		t.Fatalf("expected 'cannot be moved' error, got: %v", err)
	}
}

// TestResolve_FieldRenameAllowed tests that fields can be renamed.
func TestResolve_FieldRenameAllowed(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct { F int }\n",
	})

	fieldIdent := findIdentInIndex(ix, "F")
	if fieldIdent == nil {
		t.Fatal("field F not found")
	}

	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{Ident: fieldIdent, Rename: "G"}}, nil, plan)
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
	t.Parallel()

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
	resolved, err := resolve(ix, relos, nil, plan)
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
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype Foo struct{}\n\nfunc NewFoo() *Foo { return &Foo{} }\n",
	})

	typeIdent := findDefIdentInIndex(ix, "Foo")
	if typeIdent == nil {
		t.Fatal("type Foo not found")
	}

	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	plan := &Plan{}
	_, err := resolve(ix, []Relo{{
		Ident:  typeIdent,
		MoveTo: filepath.Join(pkgDir, "target.go"),
	}}, nil, plan)
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
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct{}\n\nfunc (t T) M() {}\n\nfunc (t *T) N() {}\n",
	})

	typeIdent := findDefIdentInIndex(ix, "T")
	if typeIdent == nil {
		t.Fatal("type T not found")
	}

	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{
		Ident:  typeIdent,
		MoveTo: filepath.Join(pkgDir, "target.go"),
	}}, nil, plan)
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

// TestResolve_ConflictingExplicitRelos tests that two explicit relos
// for the same group with different targets produce an error.
func TestResolve_ConflictingExplicitRelos(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X = 1\n",
	})

	varIdent := findDefIdentInIndex(ix, "X")
	if varIdent == nil {
		t.Fatal("var X not found")
	}

	plan := &Plan{}
	relos := []Relo{
		{Ident: varIdent, MoveTo: "/tmp/a.go"},
		{Ident: varIdent, MoveTo: "/tmp/b.go"},
	}
	_, err := resolve(ix, relos, nil, plan)
	if !errContains(err, "conflicting relos") {
		t.Fatalf("expected 'conflicting relos' error, got: %v", err)
	}
}

// TestResolve_InvalidRenameTarget tests that invalid Go identifiers
// are rejected as rename targets.
func TestResolve_InvalidRenameTarget(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X = 1\n",
	})

	varIdent := findDefIdentInIndex(ix, "X")
	if varIdent == nil {
		t.Fatal("var X not found")
	}

	tests := []struct {
		name    string
		rename  string
		wantErr bool
	}{
		{"valid", "Y", false},
		{"starts with digit", "123bad", true},
		{"keyword", "func", true},
		{"empty", "", false}, // empty means no rename
		{"underscore", "_", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan := &Plan{}
			_, err := resolve(ix, []Relo{{Ident: varIdent, Rename: tt.rename}}, nil, plan)
			if tt.wantErr && !errContains(err, "not a valid Go identifier") {
				t.Errorf("expected 'not a valid Go identifier' error, got: %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestResolve_NilIdent tests that resolve returns a clear error for nil Ident.
func TestResolve_NilIdent(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X = 1\n",
	})

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: nil}}, nil, plan)
	if !errContains(err, "nil Ident") {
		t.Fatalf("expected 'nil Ident' error, got: %v", err)
	}
}

// TestResolve_RenameInitWarning tests that renaming an init function
// produces a warning about losing auto-execution semantics.
func TestResolve_RenameInitWarning(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nfunc init() {}\n",
	})

	initIdent := findDefIdentInIndex(ix, "init")
	if initIdent == nil {
		t.Fatal("init not found")
	}

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: initIdent, Rename: "Setup"}}, nil, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(plan, "loses automatic execution") {
		t.Errorf("expected warning about losing init semantics, got: %v", plan.Warnings)
	}
}

// TestResolve_RenameToInitWarning tests that renaming a function to "init"
// produces a warning about gaining auto-execution semantics.
func TestResolve_RenameToInitWarning(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nfunc Setup() {}\n",
	})

	ident := findDefIdentInIndex(ix, "Setup")
	if ident == nil {
		t.Fatal("Setup not found")
	}

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: ident, Rename: "init"}}, nil, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(plan, "gains automatic execution") {
		t.Errorf("expected warning about gaining init semantics, got: %v", plan.Warnings)
	}
}

// TestResolve_RenameMainWarning tests that renaming the main function in a
// main package produces a warning about losing entry-point semantics.
func TestResolve_RenameMainWarning(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	})

	mainIdent := findDefIdentInIndex(ix, "main")
	if mainIdent == nil {
		t.Fatal("main not found")
	}

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: mainIdent, Rename: "Run"}}, nil, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(plan, "entry-point") {
		t.Errorf("expected warning about entry-point semantics, got: %v", plan.Warnings)
	}
}

// TestResolve_RenameToMainWarning tests that renaming a function to "main"
// in a main package produces a warning about gaining entry-point semantics.
func TestResolve_RenameToMainWarning(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package main\n\nfunc Run() {}\n",
	})

	ident := findDefIdentInIndex(ix, "Run")
	if ident == nil {
		t.Fatal("Run not found")
	}

	plan := &Plan{}
	_, err := resolve(ix, []Relo{{Ident: ident, Rename: "main"}}, nil, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(plan, "gains entry-point") {
		t.Errorf("expected warning about gaining entry-point semantics, got: %v", plan.Warnings)
	}
}

// TestResolve_SynthesizedUnexportedMethodCrossPackage tests that synthesized
// unexported methods are moved with the type cross-package. Methods with no
// external callers stay unexported; methods called from outside the type's
// own methods are auto-exported.
func TestResolve_SynthesizedUnexportedMethodCrossPackage(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct{}\n\nfunc (t T) Exported() {}\n\nfunc (t T) helper() {}\n\nfunc CallHelper(v T) { v.helper() }\n",
	})

	typeIdent := findDefIdentInIndex(ix, "T")
	if typeIdent == nil {
		t.Fatal("type T not found")
	}

	// Move cross-package (different directory).
	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{
		Ident:  typeIdent,
		MoveTo: "/tmp/otherpkg/target.go",
	}}, nil, plan)
	if err != nil {
		t.Fatal(err)
	}

	// helper is called from CallHelper (a standalone func, not a sibling
	// method), so it should be auto-exported.
	found := false
	for _, rr := range resolved {
		if rr.Group.Name == "helper" {
			found = true
			if rr.TargetName != "Helper" {
				t.Errorf("unexported method 'helper' should be auto-exported to 'Helper', got %q", rr.TargetName)
			}
		}
	}
	if !found {
		t.Errorf("unexported method 'helper' should have been synthesized for cross-package move")
	}
}

// TestResolve_SynthesizedUnexportedMethodNoExternalUse tests that an
// unexported method with no callers outside the type stays unexported.
func TestResolve_SynthesizedUnexportedMethodNoExternalUse(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\ntype T struct{}\n\nfunc (t T) Exported() { t.helper() }\n\nfunc (t T) helper() {}\n",
	})

	typeIdent := findDefIdentInIndex(ix, "T")
	if typeIdent == nil {
		t.Fatal("type T not found")
	}

	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{
		Ident:  typeIdent,
		MoveTo: "/tmp/otherpkg/target.go",
	}}, nil, plan)
	if err != nil {
		t.Fatal(err)
	}

	// helper is only called from Exported (a sibling method), so it
	// should stay unexported.
	for _, rr := range resolved {
		if rr.Group.Name == "helper" {
			if rr.TargetName != "helper" {
				t.Errorf("unexported method 'helper' should stay unexported, got %q", rr.TargetName)
			}
			return
		}
	}
	t.Errorf("unexported method 'helper' should have been synthesized")
}

// TestResolve_UnexportedSamePackageMove tests that unexported declarations
// can be moved to a different file within the same package, even when the
// target path is relative (the common case from rules files).
func TestResolve_UnexportedSamePackageMove(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar secret = 42\n\nfunc use() int { return secret }\n",
	})

	varIdent := findDefIdentInIndex(ix, "secret")
	if varIdent == nil {
		t.Fatal("var secret not found")
	}

	// Use a path in the same directory as the package files — simulating
	// what FromRules produces with filepath.Join(".", "target.go").
	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	target := filepath.Join(pkgDir, "target.go")

	plan := &Plan{}
	resolved, err := resolve(ix, []Relo{{Ident: varIdent, MoveTo: target}}, nil, plan)
	if err != nil {
		t.Fatalf("unexported same-package move should succeed, got: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved relo, got %d", len(resolved))
	}
	if resolved[0].TargetName != "secret" {
		t.Errorf("name should stay unexported, got %q", resolved[0].TargetName)
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
