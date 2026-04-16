package relo

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestGuessImportLocalName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{"fmt", "fmt"},
		{"math/rand", "rand"},
		{"crypto/rand", "rand"},
		{"encoding/json", "json"},
		{"github.com/foo/bar", "bar"},
		{"github.com/foo/bar/v2", "bar"},           // versioned
		{"github.com/foo/bar/v3", "bar"},           // versioned
		{"github.com/mattn/go-ieproxy", "ieproxy"}, // go- prefix stripped
		{"github.com/foo/go_bar", "bar"},           // go_ prefix stripped
		{"github.com/foo/my-lib", "mylib"},         // dash removed
		{"github.com/foo/my.lib", "mylib"},         // dot removed
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			got := guessImportLocalName(tt.path)
			if got != tt.want {
				t.Errorf("guessImportLocalName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestParentPrefixedName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{"math/rand", "mathrand"},
		{"crypto/rand", "cryptorand"},
		{"encoding/json", "encodingjson"},
		{"fmt", "fmt"}, // no parent
		{"github.com/foo/bar", "foobar"},
		{"github.com/foo-bar/baz", "foobar" + "baz"}, // dash in parent removed
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			got := parentPrefixedName(tt.path)
			if got != tt.want {
				t.Errorf("parentPrefixedName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestGuessImportPath(t *testing.T) {
	t.Parallel()

	// Create a temp directory with a go.mod.
	dir := t.TempDir()
	modContent := "module example.com/mymod\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Root directory.
	got := guessImportPath(dir)
	if got != "example.com/mymod" {
		t.Errorf("guessImportPath(root) = %q, want %q", got, "example.com/mymod")
	}

	// Subdirectory.
	subDir := filepath.Join(dir, "sub", "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	got = guessImportPath(subDir)
	if got != "example.com/mymod/sub/pkg" {
		t.Errorf("guessImportPath(sub/pkg) = %q, want %q", got, "example.com/mymod/sub/pkg")
	}
}

func TestGuessImportPath_NoGoMod(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	got := guessImportPath(dir)
	if got != "" {
		t.Errorf("guessImportPath(no go.mod) = %q, want empty", got)
	}
}

func TestReadModulePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// No go.mod.
	got := mast.ReadModulePath(dir)
	if got != "" {
		t.Errorf("ReadModulePath(no go.mod) = %q, want empty", got)
	}

	// With go.mod.
	modContent := "module example.com/mymod\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}
	got = mast.ReadModulePath(dir)
	if got != "example.com/mymod" {
		t.Errorf("ReadModulePath = %q, want %q", got, "example.com/mymod")
	}
}

func TestAddImportEntry_FirstKeepsShortName(t *testing.T) {
	t.Parallel()

	// When two imports share a localName, the first keeps the short
	// name; subsequent ones get a parent-prefixed alias. Equivalent
	// to the former resolveCollisions behavior, expressed via the
	// addImportEntry path.
	is := &importSet{byFile: make(map[string]*importChange)}
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "crypto/rand"})
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "math/rand"})

	ic := is.byFile["/dst/file.go"]
	if ic == nil || len(ic.Add) != 2 {
		t.Fatalf("want 2 added entries, got %v", ic)
	}
	if ic.Add[0].Alias != "" {
		t.Errorf("crypto/rand should keep short name, got alias %q", ic.Add[0].Alias)
	}
	if ic.Add[1].Alias != "mathrand" {
		t.Errorf("math/rand alias = %q, want %q", ic.Add[1].Alias, "mathrand")
	}
}

func TestAddImportEntry_NumericSuffixWhenParentPrefixUsed(t *testing.T) {
	t.Parallel()

	// When parentPrefixedName is already taken, a numeric suffix is
	// generated.
	is := &importSet{byFile: make(map[string]*importChange)}
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "aaa/rand"})
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "bbb/rand"})
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "ccc/rand"})

	ic := is.byFile["/dst/file.go"]
	if ic == nil || len(ic.Add) != 3 {
		t.Fatalf("want 3 added entries, got %v", ic)
	}
	if ic.Add[0].Alias != "" {
		t.Errorf("aaa/rand should keep short name, got alias %q", ic.Add[0].Alias)
	}
	if ic.Add[1].Alias != "bbbrand" {
		t.Errorf("bbb/rand alias = %q, want %q", ic.Add[1].Alias, "bbbrand")
	}
	if ic.Add[2].Alias != "cccrand" {
		t.Errorf("ccc/rand alias = %q, want %q", ic.Add[2].Alias, "cccrand")
	}
}

func TestAddImportEntry_NoCollision(t *testing.T) {
	t.Parallel()

	is := &importSet{byFile: make(map[string]*importChange)}
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "encoding/json"})
	addImportEntry(is, nil, "/dst/file.go", importEntry{Path: "fmt"})

	ic := is.byFile["/dst/file.go"]
	if ic == nil || len(ic.Add) != 2 {
		t.Fatalf("want 2 added entries, got %v", ic)
	}
	for _, e := range ic.Add {
		if e.Alias != "" {
			t.Errorf("non-colliding import %q got unexpected alias %q", e.Path, e.Alias)
		}
	}
}

func TestPackageLocalName(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	libDir := filepath.Join(base, "lib")

	// Index with a package named "mylib" in directory "lib".
	mainFile := mastFileWithSyntax(filepath.Join(libDir, "lib.go"), "mylib")
	mainPkg := &mast.Package{
		Name:  "mylib",
		Files: []*mast.File{mainFile},
	}

	// Also has a _test package in the same directory.
	testFile := mastFileWithSyntax(filepath.Join(libDir, "lib_test.go"), "mylib_test")
	testPkg := &mast.Package{
		Name:  "mylib_test",
		Files: []*mast.File{testFile},
	}

	ix := &mast.Index{
		Pkgs: []*mast.Package{testPkg, mainPkg}, // _test first
	}

	got := packageLocalName(ix, libDir)
	if got != "mylib" {
		t.Errorf("packageLocalName = %q, want %q", got, "mylib")
	}
}

func TestPackageLocalName_Integration(t *testing.T) {
	t.Parallel()

	// Create a module with a package whose name differs from the dir.
	dir := t.TempDir()
	modContent := "module example.com/test\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "lib.go"), []byte("package mylib\n\nfunc F() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ix, err := mast.Load(&mast.Config{Dir: dir}, "./...")
	if err != nil {
		t.Fatal("mast.Load:", err)
	}

	// Verify that lib package is loaded.
	found := false
	for _, pkg := range ix.Pkgs {
		if pkg.Name == "mylib" {
			found = true
			t.Logf("found pkg %q with dir %q", pkg.Name, filepath.Dir(pkg.Files[0].Path))
			break
		}
	}
	if !found {
		t.Log("packages in index:")
		for _, pkg := range ix.Pkgs {
			dir := ""
			if len(pkg.Files) > 0 {
				dir = filepath.Dir(pkg.Files[0].Path)
			}
			t.Logf("  pkg=%q path=%q dir=%q", pkg.Name, pkg.Path, dir)
		}
		t.Fatal("mylib package not found in index")
	}

	got := packageLocalName(ix, libDir)
	if got != "mylib" {
		t.Errorf("packageLocalName = %q, want %q", got, "mylib")
	}
}

func TestPackageLocalName_Fallback(t *testing.T) {
	t.Parallel()

	// When the directory has no packages in the index, fall back to
	// guessImportLocalName.
	dir := t.TempDir()
	modContent := "module example.com/test\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "mylib")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	ix := &mast.Index{}
	got := packageLocalName(ix, subDir)
	if got != "mylib" {
		t.Errorf("packageLocalName fallback = %q, want %q", got, "mylib")
	}
}

func TestBlankImportWarning(t *testing.T) {
	t.Parallel()

	// E5: Blank imports (import _ "pkg") should emit a warning.
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write go.mod.
	modContent := "module example.com/blanktest\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Source file with a blank import and a normal import.
	src := `package pkg

import (
	"fmt"
	_ "image/png"
)

func Hello() {
	fmt.Println("hello")
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "source.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	ix, err := mast.Load(&mast.Config{Dir: pkgDir}, ".")
	if err != nil {
		t.Fatal("loading package:", err)
	}

	// Find the Hello ident.
	var helloIdent *ast.Ident
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Syntax.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "Hello" {
					helloIdent = fd.Name
				}
			}
		}
	}
	if helloIdent == nil {
		t.Fatal("Hello ident not found")
	}

	plan, err := Compile(ix, []Relo{{
		Ident:  helloIdent,
		MoveTo: filepath.Join(pkgDir, "target.go"),
	}}, nil, nil)
	if err != nil {
		t.Fatal("compile:", err)
	}

	// Check that a warning about blank import was emitted.
	found := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Message, "blank import") && strings.Contains(w.Message, "image/png") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected warning about blank import _ \"image/png\", but none found")
		for _, w := range plan.Warnings {
			t.Logf("  warning: %s", w.Message)
		}
	}
}
