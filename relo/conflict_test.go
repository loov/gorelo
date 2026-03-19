package relo

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestConstraintsMayOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// Empty = always active.
		{"", "", true},
		{"", "//go:build linux", true},
		{"//go:build linux", "", true},

		// Same constraint.
		{"//go:build linux", "//go:build linux", true},

		// Negation pairs.
		{"//go:build linux", "//go:build !linux", false},
		{"//go:build !linux", "//go:build linux", false},

		// Exclusive OS tags.
		{"//go:build linux", "//go:build darwin", false},
		{"//go:build windows", "//go:build linux", false},
		{"//go:build freebsd", "//go:build openbsd", false},

		// Exclusive arch tags.
		{"//go:build amd64", "//go:build arm64", false},
		{"//go:build 386", "//go:build wasm", false},

		// Non-exclusive tags.
		{"//go:build cgo", "//go:build race", true},

		// Complex constraints — conservative overlap.
		{"//go:build linux && amd64", "//go:build darwin", true},
		{"//go:build (linux || darwin)", "//go:build windows", true},

		// OS vs arch — not mutually exclusive categories.
		{"//go:build linux", "//go:build amd64", true},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := constraintsMayOverlap(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("constraintsMayOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestExtractConstraintTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"//go:build linux", "linux"},
		{"//go:build !linux", "!linux"},
		{"//go:build amd64", "amd64"},
		{"//go:build linux && amd64", ""},   // compound
		{"//go:build linux || darwin", ""},   // compound
		{"//go:build (linux)", ""},           // parentheses
		{"//go:build !linux && !darwin", ""}, // compound negation
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractConstraintTag(tt.input)
			if got != tt.want {
				t.Errorf("extractConstraintTag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNameConflicts(t *testing.T) {
	src := `package p

type Foo struct{}

func Bar() {}

var Baz = 1

const Qux = "hello"
`
	file, _ := parseSource(t, src)

	tests := []struct {
		name string
		want bool
	}{
		{"Foo", true},
		{"Bar", true},
		{"Baz", true},
		{"Qux", true},
		{"NotThere", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, decl := range file.Decls {
				if nameConflicts(decl, tt.name) {
					if !tt.want {
						t.Errorf("nameConflicts(%q) found conflict unexpectedly", tt.name)
					}
					return
				}
			}
			if tt.want {
				t.Errorf("nameConflicts(%q) did not find expected conflict", tt.name)
			}
		})
	}
}

func TestNameConflicts_Method(t *testing.T) {
	file, _ := parseSource(t, "package p\n\ntype T struct{}\nfunc (t T) M() {}\n")

	// Methods should not conflict (they have receivers).
	for _, decl := range file.Decls {
		if nameConflicts(decl, "M") {
			t.Error("method M should not conflict (has receiver)")
		}
	}
}

func TestHasDirective(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		directive string
		want      bool
	}{
		{
			name: "embed on var",
			src: `package p

//go:embed file.txt
var content string
`,
			directive: "go:embed",
			want:      true,
		},
		{
			name: "generate on func",
			src: `package p

//go:generate mockgen -source=foo.go
func Foo() {}
`,
			directive: "go:generate",
			want:      true,
		},
		{
			name: "no directive",
			src: `package p

// Regular comment.
func Bar() {}
`,
			directive: "go:embed",
			want:      false,
		},
		{
			name: "directive in doc comment",
			src: `package p

// Foo does things.
//go:generate echo hello
func Foo() {}
`,
			directive: "go:generate",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, fset := parseSource(t, tt.src)

			// Find the first non-import decl.
			for _, decl := range file.Decls {
				if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
					continue
				}
				got := hasDirective(decl, file, fset, tt.directive)
				if got != tt.want {
					t.Errorf("hasDirective(%q) = %v, want %v", tt.directive, got, tt.want)
				}
				return
			}
			t.Fatal("no declaration found")
		})
	}
}

func TestCheckConstraints_MixedWarning(t *testing.T) {
	plan := &Plan{}

	pkg := &mast.Package{Name: "p", Path: "example.com/p"}
	resolved := []*resolvedRelo{
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "//go:build linux", Pkg: pkg},
		},
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "//go:build darwin", Pkg: pkg},
		},
	}

	checkConstraints(resolved, plan)

	if !hasWarning(plan, "mixed build constraints") {
		t.Errorf("expected 'mixed build constraints' warning, got: %v", plan.Warnings)
	}
}

func TestCheckConstraints_NoWarningForSameConstraint(t *testing.T) {
	plan := &Plan{}

	pkg := &mast.Package{Name: "p", Path: "example.com/p"}
	resolved := []*resolvedRelo{
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "//go:build linux", Pkg: pkg},
		},
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "//go:build linux", Pkg: pkg},
		},
	}

	checkConstraints(resolved, plan)

	if len(plan.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", plan.Warnings)
	}
}

func TestCheckConstraints_NoWarningForUnconstrained(t *testing.T) {
	plan := &Plan{}

	pkg := &mast.Package{Name: "p", Path: "example.com/p"}
	resolved := []*resolvedRelo{
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "", Pkg: pkg},
		},
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "", Pkg: pkg},
		},
	}

	checkConstraints(resolved, plan)

	if len(plan.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", plan.Warnings)
	}
}
