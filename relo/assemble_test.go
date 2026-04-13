package relo

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestEnsureImport_GroupedBlock(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	"fmt"
)

func Foo() { fmt.Println() }
`
	got, _ := ensureImport(src, importEntry{Path: "strings"})
	if !strings.Contains(got, `"strings"`) {
		t.Errorf("expected import to be added, got:\n%s", got)
	}
	// Should still have fmt.
	if !strings.Contains(got, `"fmt"`) {
		t.Errorf("expected fmt to be preserved, got:\n%s", got)
	}
}

func TestEnsureImport_SingleImport(t *testing.T) {
	t.Parallel()

	src := `package p

import "fmt"

func Foo() { fmt.Println() }
`
	got, _ := ensureImport(src, importEntry{Path: "strings"})
	if !strings.Contains(got, `"strings"`) {
		t.Errorf("expected strings import to be added, got:\n%s", got)
	}
	// Should be converted to grouped block.
	if !strings.Contains(got, "import (") {
		t.Errorf("expected grouped import block, got:\n%s", got)
	}
}

func TestEnsureImport_NoExistingImport(t *testing.T) {
	t.Parallel()

	src := `package p

func Foo() {}
`
	got, _ := ensureImport(src, importEntry{Path: "fmt"})
	if !strings.Contains(got, `"fmt"`) {
		t.Errorf("expected fmt import to be added, got:\n%s", got)
	}
	if !strings.Contains(got, "import (") {
		t.Errorf("expected import block to be created, got:\n%s", got)
	}
}

func TestEnsureImport_WithAlias(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	"fmt"
)

func Foo() { fmt.Println() }
`
	got, _ := ensureImport(src, importEntry{Path: "math/rand", Alias: "mathrand"})
	if !strings.Contains(got, `mathrand "math/rand"`) {
		t.Errorf("expected aliased import, got:\n%s", got)
	}
}

func TestEnsureImport_AlreadyExists(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	"fmt"
)

func Foo() { fmt.Println() }
`
	got, _ := ensureImport(src, importEntry{Path: "fmt"})
	// Should not duplicate.
	count := strings.Count(got, `"fmt"`)
	if count != 1 {
		t.Errorf("expected exactly 1 fmt import, got %d in:\n%s", count, got)
	}
}

func TestEnsureImport_AliasMismatchWarning(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	foo "example.com/bar"
)

func Foo() { foo.X() }
`
	_, warn := ensureImport(src, importEntry{Path: "example.com/bar", Alias: "baz"})
	if warn.Message == "" {
		t.Error("expected alias mismatch warning")
	}
	if !strings.Contains(warn.Message, "alias") {
		t.Errorf("warning should mention alias, got: %s", warn.Message)
	}
}

func TestRemoveUnusedImportsText(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	"fmt"
	"strings"
)

func Foo() { fmt.Println() }
`
	got := removeUnusedImportsText(src)
	if strings.Contains(got, "strings") {
		t.Errorf("expected strings import to be removed, got:\n%s", got)
	}
	if !strings.Contains(got, "fmt") {
		t.Errorf("expected fmt import to be kept, got:\n%s", got)
	}
}

func TestRemoveUnusedImportsText_BlankImportKept(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	_ "embed"
	"fmt"
)

func Foo() { fmt.Println() }
`
	got := removeUnusedImportsText(src)
	if !strings.Contains(got, `_ "embed"`) {
		t.Errorf("expected blank import to be kept, got:\n%s", got)
	}
}

func TestRemoveUnusedImportsText_AllUsed(t *testing.T) {
	t.Parallel()

	src := `package p

import (
	"fmt"
	"strings"
)

func Foo() { fmt.Println(); strings.Contains("a", "b") }
`
	got := removeUnusedImportsText(src)
	if !strings.Contains(got, "fmt") || !strings.Contains(got, "strings") {
		t.Errorf("expected all imports kept, got:\n%s", got)
	}
}

func TestRemoveEmptyDeclBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		src     string
		wantNot string // substring that should be absent
		wantHas string // substring that should be present
	}{
		{
			name:    "empty import block",
			src:     "package p\n\nimport (\n)\n\nfunc F() {}\n",
			wantNot: "import (",
			wantHas: "func F()",
		},
		{
			name:    "empty const block",
			src:     "package p\n\nconst (\n)\n\nfunc F() {}\n",
			wantNot: "const (",
			wantHas: "func F()",
		},
		{
			name:    "empty var block",
			src:     "package p\n\nvar (\n)\n\nfunc F() {}\n",
			wantNot: "var (",
			wantHas: "func F()",
		},
		{
			name:    "non-empty preserved",
			src:     "package p\n\nimport (\n\t\"fmt\"\n)\n\nfunc F() { fmt.Println() }\n",
			wantNot: "",
			wantHas: "import (",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := removeEmptyDeclBlocks(tt.src)
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Errorf("removeEmptyDeclBlocks: should not contain %q, got:\n%s", tt.wantNot, got)
			}
			if tt.wantHas != "" && !strings.Contains(got, tt.wantHas) {
				t.Errorf("removeEmptyDeclBlocks: should contain %q, got:\n%s", tt.wantHas, got)
			}
		})
	}
}

func TestCleanBlankLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "three blank lines collapsed to two",
			src:  "a\n\n\n\nb",
			want: "a\n\n\nb",
		},
		{
			name: "two blank lines kept",
			src:  "a\n\n\nb",
			want: "a\n\n\nb",
		},
		{
			name: "no blank lines unchanged",
			src:  "a\nb\nc",
			want: "a\nb\nc",
		},
		{
			name: "many blank lines collapsed",
			src:  "a\n\n\n\n\n\nb",
			want: "a\n\n\nb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := cleanBlankLines(tt.src)
			if got != tt.want {
				t.Errorf("cleanBlankLines(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestSourceFileIsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "package only",
			src:  "package p\n",
			want: true,
		},
		{
			name: "package with import",
			src:  "package p\n\nimport \"fmt\"\n",
			want: true,
		},
		{
			name: "has func",
			src:  "package p\n\nfunc F() {}\n",
			want: false,
		},
		{
			name: "has type",
			src:  "package p\n\ntype T struct{}\n",
			want: false,
		},
		{
			name: "has var",
			src:  "package p\n\nvar X = 1\n",
			want: false,
		},
		{
			name: "has const",
			src:  "package p\n\nconst Y = 2\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sourceFileIsEmpty(tt.src)
			if got != tt.want {
				t.Errorf("sourceFileIsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGuessPackageName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dir  string
		want string
	}{
		{"/home/user/project/pkg", "pkg"},
		{"/home/user/project/my-pkg", "mypkg"},
		{"/home/user/project/my.pkg", "mypkg"},
		{"/home/user/project", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			t.Parallel()

			got := guessPackageName(tt.dir)
			if got != tt.want {
				t.Errorf("guessPackageName(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

func TestCollectBuildConstraint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tags []string
		want string
	}{
		{
			name: "all unconstrained",
			tags: []string{"", ""},
			want: "",
		},
		{
			name: "all same constraint",
			tags: []string{"//go:build linux", "//go:build linux"},
			want: "//go:build linux",
		},
		{
			name: "mixed constraints",
			tags: []string{"//go:build linux", "//go:build darwin"},
			want: "",
		},
		{
			name: "constrained and unconstrained",
			tags: []string{"//go:build linux", ""},
			want: "",
		},
		{
			name: "single constrained",
			tags: []string{"//go:build amd64"},
			want: "//go:build amd64",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var rrs []*resolvedRelo
			for _, tag := range tt.tags {
				rrs = append(rrs, &resolvedRelo{
					File: mastFileWithTag(tag),
				})
			}
			got := collectBuildConstraint(rrs)
			if got != tt.want {
				t.Errorf("collectBuildConstraint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetermineTargetPkgName_SameDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	pkgDir := filepath.Join(base, "pkg")

	file := mastFileWithSyntax(filepath.Join(pkgDir, "source.go"), "mypkg")
	file.Pkg = &mast.Package{
		Name:  "mypkg",
		Files: []*mast.File{file},
	}
	rrs := []*resolvedRelo{
		{
			File:       file,
			TargetFile: filepath.Join(pkgDir, "target.go"),
		},
	}
	ix := &mast.Index{}
	got := determineTargetPkgName(ix, rrs)
	if got != "mypkg" {
		t.Errorf("determineTargetPkgName = %q, want %q", got, "mypkg")
	}
}

func TestDetermineTargetPkgName_DifferentDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	rrs := []*resolvedRelo{
		{
			File:       mastFileWithSyntax(filepath.Join(base, "src", "source.go"), "srcpkg"),
			TargetFile: filepath.Join(base, "dst", "target.go"),
		},
	}
	ix := &mast.Index{}
	got := determineTargetPkgName(ix, rrs)
	if got != "dst" {
		t.Errorf("determineTargetPkgName = %q, want %q", got, "dst")
	}
}

func TestSortedKeys(t *testing.T) {
	t.Parallel()

	m := map[string]int{
		"cherry": 3,
		"apple":  1,
		"banana": 2,
	}
	got := sortedKeys(m)
	want := []string{"apple", "banana", "cherry"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys returned %d keys, want %d", len(got), len(want))
	}
	for i, k := range got {
		if k != want[i] {
			t.Errorf("sortedKeys[%d] = %q, want %q", i, k, want[i])
		}
	}
}

func TestAssemble_SourceTargetOverlap(t *testing.T) {
	t.Parallel()

	// Scenario: File B.go has declaration Y. We move X from A.go → B.go and
	// move Y from B.go → C.go. B.go is both a source (Y leaves) and a target
	// (X arrives). The result should be a single consistent edit for B.go that
	// contains X but not Y.
	ix := loadTestIndex(t, map[string]string{
		"a.go": "package p\n\nfunc X() {}\n",
		"b.go": "package p\n\nfunc Y() {}\n",
	})

	identX := findDefIdentInIndex(ix, "X")
	identY := findDefIdentInIndex(ix, "Y")
	if identX == nil || identY == nil {
		t.Fatal("could not find X or Y idents")
	}

	// Build the target paths using the actual temp directory.
	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	bPath := filepath.Join(pkgDir, "b.go")
	cPath := filepath.Join(pkgDir, "c.go")

	plan, err := Compile(ix, []Relo{
		{Ident: identX, MoveTo: bPath},
		{Ident: identY, MoveTo: cPath},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Check that B.go has exactly one edit that contains X but not Y.
	bEditCount := 0
	for _, fe := range plan.Edits {
		if fe.Path == bPath {
			bEditCount++
			if fe.IsDelete {
				t.Error("B.go should not be deleted")
				continue
			}
			if !strings.Contains(fe.Content, "func X()") {
				t.Errorf("B.go should contain func X, got:\n%s", fe.Content)
			}
			if strings.Contains(fe.Content, "func Y()") {
				t.Errorf("B.go should not contain func Y, got:\n%s", fe.Content)
			}
		}
	}
	if bEditCount != 1 {
		t.Errorf("expected exactly 1 edit for B.go, got %d", bEditCount)
	}

	// Check that C.go exists and contains Y.
	foundC := false
	for _, fe := range plan.Edits {
		if fe.Path == cPath {
			foundC = true
			if !strings.Contains(fe.Content, "func Y()") {
				t.Errorf("C.go should contain func Y, got:\n%s", fe.Content)
			}
		}
	}
	if !foundC {
		t.Error("expected C.go edit to be created")
	}
}

// TestAssemble_SamePackageMoveRemovesFromSource tests that moving a
// declaration to a different file within the same package removes it
// from the source file. This was a bug where same-package moves created
// the target file but left the declaration duplicated in the source.
func TestAssemble_SamePackageMoveRemovesFromSource(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"source.go": "package p\n\nvar x = 1\n\nvar y = 2\n",
	})

	identX := findDefIdentInIndex(ix, "x")
	if identX == nil {
		t.Fatal("var x not found")
	}

	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	targetPath := filepath.Join(pkgDir, "target.go")
	sourcePath := filepath.Join(pkgDir, "source.go")

	plan, err := Compile(ix, []Relo{
		{Ident: identX, MoveTo: targetPath},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// target.go should contain x.
	targetFound := false
	for _, fe := range plan.Edits {
		if fe.Path == targetPath {
			targetFound = true
			if !strings.Contains(fe.Content, "var x") {
				t.Errorf("target.go should contain var x, got:\n%s", fe.Content)
			}
		}
	}
	if !targetFound {
		t.Error("expected target.go edit to be created")
	}

	// source.go should no longer contain x, but should still contain y.
	for _, fe := range plan.Edits {
		if fe.Path == sourcePath {
			if strings.Contains(fe.Content, "var x") {
				t.Errorf("source.go should not contain var x after move, got:\n%s", fe.Content)
			}
			if !strings.Contains(fe.Content, "var y") {
				t.Errorf("source.go should still contain var y, got:\n%s", fe.Content)
			}
		}
	}
}

// mastFileWithTag creates a minimal mast.File with the given build tag.
func mastFileWithTag(tag string) *mast.File {
	return &mast.File{BuildTag: tag}
}

// mastFileWithSyntax creates a mast.File with path and a parsed package name.
func mastFileWithSyntax(path, pkgName string) *mast.File {
	return &mast.File{
		Path: path,
		Syntax: &ast.File{
			Name: ast.NewIdent(pkgName),
		},
	}
}
