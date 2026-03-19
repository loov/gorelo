package relo

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestEnsureImport_GroupedBlock(t *testing.T) {
	src := `package p

import (
	"fmt"
)

func Foo() { fmt.Println() }
`
	got := ensureImport(src, importEntry{Path: "strings"})
	if !strings.Contains(got, `"strings"`) {
		t.Errorf("expected import to be added, got:\n%s", got)
	}
	// Should still have fmt.
	if !strings.Contains(got, `"fmt"`) {
		t.Errorf("expected fmt to be preserved, got:\n%s", got)
	}
}

func TestEnsureImport_SingleImport(t *testing.T) {
	src := `package p

import "fmt"

func Foo() { fmt.Println() }
`
	got := ensureImport(src, importEntry{Path: "strings"})
	if !strings.Contains(got, `"strings"`) {
		t.Errorf("expected strings import to be added, got:\n%s", got)
	}
	// Should be converted to grouped block.
	if !strings.Contains(got, "import (") {
		t.Errorf("expected grouped import block, got:\n%s", got)
	}
}

func TestEnsureImport_NoExistingImport(t *testing.T) {
	src := `package p

func Foo() {}
`
	got := ensureImport(src, importEntry{Path: "fmt"})
	if !strings.Contains(got, `"fmt"`) {
		t.Errorf("expected fmt import to be added, got:\n%s", got)
	}
	if !strings.Contains(got, "import (") {
		t.Errorf("expected import block to be created, got:\n%s", got)
	}
}

func TestEnsureImport_WithAlias(t *testing.T) {
	src := `package p

import (
	"fmt"
)

func Foo() { fmt.Println() }
`
	got := ensureImport(src, importEntry{Path: "math/rand", Alias: "mathrand"})
	if !strings.Contains(got, `mathrand "math/rand"`) {
		t.Errorf("expected aliased import, got:\n%s", got)
	}
}

func TestEnsureImport_AlreadyExists(t *testing.T) {
	src := `package p

import (
	"fmt"
)

func Foo() { fmt.Println() }
`
	got := ensureImport(src, importEntry{Path: "fmt"})
	// Should not duplicate.
	count := strings.Count(got, `"fmt"`)
	if count != 1 {
		t.Errorf("expected exactly 1 fmt import, got %d in:\n%s", count, got)
	}
}

func TestRemoveUnusedImportsText(t *testing.T) {
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
			got := cleanBlankLines(tt.src)
			if got != tt.want {
				t.Errorf("cleanBlankLines(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestSourceFileIsEmpty(t *testing.T) {
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
			got := sourceFileIsEmpty(tt.src)
			if got != tt.want {
				t.Errorf("sourceFileIsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGuessPackageName(t *testing.T) {
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
			got := guessPackageName(tt.dir)
			if got != tt.want {
				t.Errorf("guessPackageName(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

func TestCollectBuildConstraint(t *testing.T) {
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
	rrs := []*resolvedRelo{
		{
			File:       mastFileWithSyntax("/tmp/pkg/source.go", "mypkg"),
			TargetFile: "/tmp/pkg/target.go",
		},
	}
	got := determineTargetPkgName(rrs)
	if got != "mypkg" {
		t.Errorf("determineTargetPkgName = %q, want %q", got, "mypkg")
	}
}

func TestDetermineTargetPkgName_DifferentDir(t *testing.T) {
	rrs := []*resolvedRelo{
		{
			File:       mastFileWithSyntax("/tmp/src/source.go", "srcpkg"),
			TargetFile: "/tmp/dst/target.go",
		},
	}
	got := determineTargetPkgName(rrs)
	if got != "dst" {
		t.Errorf("determineTargetPkgName = %q, want %q", got, "dst")
	}
}

func TestSortedKeys(t *testing.T) {
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
