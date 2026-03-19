package relo

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestConstraintsMayOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want bool
	}{
		// Empty = always active.
		{"", "", true},
		{"", "linux", true},
		{"linux", "", true},

		// Same constraint.
		{"linux", "linux", true},

		// Negation pairs.
		{"linux", "!linux", false},
		{"!linux", "linux", false},

		// Exclusive OS tags.
		{"linux", "darwin", false},
		{"windows", "linux", false},
		{"freebsd", "openbsd", false},

		// Exclusive arch tags.
		{"amd64", "arm64", false},
		{"386", "wasm", false},

		// Non-exclusive tags.
		{"cgo", "race", true},

		// Complex constraints — conservative overlap.
		{"linux && amd64", "darwin", true},
		{"(linux || darwin)", "windows", true},

		// OS vs arch — not mutually exclusive categories.
		{"linux", "amd64", true},

		// Negated tags from exclusive sets may still overlap.
		// !linux and !darwin are both true on FreeBSD → overlap.
		{"!linux", "!darwin", true},
		// !linux and darwin → darwin implies !linux → overlap.
		{"!linux", "darwin", true},
		{"darwin", "!linux", true},

		// ios implies darwin, android implies linux → not exclusive.
		{"ios", "darwin", true},
		{"darwin", "ios", true},
		{"android", "linux", true},
		{"linux", "android", true},
		// ios and android are still exclusive (different OS families).
		{"ios", "android", false},
		// ios and linux are exclusive (ios implies darwin, not linux).
		{"ios", "linux", false},
		// android and darwin are exclusive (android implies linux, not darwin).
		{"android", "darwin", false},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			t.Parallel()

			got := constraintsMayOverlap(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("constraintsMayOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestExtractConstraintTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"linux", "linux"},
		{"!linux", "!linux"},
		{"amd64", "amd64"},
		{"linux && amd64", ""},    // compound
		{"linux || darwin", ""},   // compound
		{"(linux)", ""},           // parentheses
		{"!linux && !darwin", ""}, // compound negation
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got := extractConstraintTag(tt.input)
			if got != tt.want {
				t.Errorf("extractConstraintTag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNameConflicts(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

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
	t.Parallel()

	file, _ := parseSource(t, "package p\n\ntype T struct{}\nfunc (t T) M() {}\n")

	// Methods should not conflict (they have receivers).
	for _, decl := range file.Decls {
		if nameConflicts(decl, "M") {
			t.Error("method M should not conflict (has receiver)")
		}
	}
}

func TestHasDirective(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

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
	t.Parallel()

	plan := &Plan{}

	pkg := &mast.Package{Name: "p", Path: "example.com/p"}
	resolved := []*resolvedRelo{
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "linux", Pkg: pkg},
		},
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "darwin", Pkg: pkg},
		},
	}

	checkConstraints(resolved, plan)

	if !hasWarning(plan, "mixed build constraints") {
		t.Errorf("expected 'mixed build constraints' warning, got: %v", plan.Warnings)
	}
}

func TestCheckConstraints_NoWarningForSameConstraint(t *testing.T) {
	t.Parallel()

	plan := &Plan{}

	pkg := &mast.Package{Name: "p", Path: "example.com/p"}
	resolved := []*resolvedRelo{
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "linux", Pkg: pkg},
		},
		{
			TargetFile: "/tmp/target.go",
			File:       &mast.File{BuildTag: "linux", Pkg: pkg},
		},
	}

	checkConstraints(resolved, plan)

	if len(plan.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", plan.Warnings)
	}
}

func testResolvedRelo(grp *mast.Group, targetFile, targetName string, file *mast.File) *resolvedRelo {
	fakeIdent := ast.NewIdent(grp.Name)
	return &resolvedRelo{
		Group:      grp,
		DefIdent:   &mast.Ident{Ident: fakeIdent, Kind: mast.Def, File: file},
		TargetFile: targetFile,
		TargetName: targetName,
		File:       file,
	}
}

func TestDetectConflicts_InterReloCollision(t *testing.T) {
	t.Parallel()

	// Two relos with the same TargetName going to the same directory
	// from different groups should produce an error.
	grpA := &mast.Group{Name: "Foo", Kind: mast.TypeName, Pkg: "example.com/a"}
	grpB := &mast.Group{Name: "Bar", Kind: mast.TypeName, Pkg: "example.com/b"}

	emptyFile := &ast.File{Name: ast.NewIdent("p")}
	ix := &mast.Index{Fset: token.NewFileSet()}
	plan := &Plan{}

	fileA := &mast.File{Path: "/tmp/a/a.go", Syntax: emptyFile, Pkg: &mast.Package{Path: "example.com/a"}}
	fileB := &mast.File{Path: "/tmp/b/b.go", Syntax: emptyFile, Pkg: &mast.Package{Path: "example.com/b"}}

	resolved := []*resolvedRelo{
		testResolvedRelo(grpA, "/tmp/target/target.go", "Foo", fileA),
		testResolvedRelo(grpB, "/tmp/target/other.go", "Foo", fileB),
	}

	err := detectConflicts(ix, resolved, nil, plan)
	if !errContains(err, "name collision") {
		t.Fatalf("expected 'name collision' error for inter-relo collision, got: %v", err)
	}
}

func TestDetectConflicts_InterReloCollision_NonOverlappingConstraints(t *testing.T) {
	t.Parallel()

	// Two relos with the same TargetName but non-overlapping build constraints
	// should NOT produce an error.
	grpA := &mast.Group{Name: "Foo", Kind: mast.TypeName, Pkg: "example.com/a"}
	grpB := &mast.Group{Name: "Bar", Kind: mast.TypeName, Pkg: "example.com/b"}

	emptyFile := &ast.File{Name: ast.NewIdent("p")}
	ix := &mast.Index{Fset: token.NewFileSet()}
	plan := &Plan{}

	fileA := &mast.File{Path: "/tmp/a/a.go", BuildTag: "linux", Syntax: emptyFile, Pkg: &mast.Package{Path: "example.com/a"}}
	fileB := &mast.File{Path: "/tmp/b/b.go", BuildTag: "darwin", Syntax: emptyFile, Pkg: &mast.Package{Path: "example.com/b"}}

	resolved := []*resolvedRelo{
		testResolvedRelo(grpA, "/tmp/target/target.go", "Foo", fileA),
		testResolvedRelo(grpB, "/tmp/target/other.go", "Foo", fileB),
	}

	err := detectConflicts(ix, resolved, nil, plan)
	if err != nil {
		t.Fatalf("expected no error for non-overlapping constraints, got: %v", err)
	}
}

func TestCheckConstraints_NoWarningForUnconstrained(t *testing.T) {
	t.Parallel()

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

func TestDetectConflicts_CircularImport_NewTargetFile(t *testing.T) {
	t.Parallel()

	// E3: When the target file doesn't exist yet, the circular import
	// check should still work by looking up the package from the target
	// directory via findPkgForDir.

	ix := &mast.Index{Fset: token.NewFileSet()}

	targetSyntax := &ast.File{
		Name:    ast.NewIdent("target"),
		Imports: []*ast.ImportSpec{{Path: &ast.BasicLit{Kind: token.STRING, Value: `"example.com/src"`}}},
	}
	targetPkg := &mast.Package{
		Name:  "target",
		Path:  "example.com/target",
		Files: []*mast.File{{Path: filepath.FromSlash("/proj/target/existing.go"), Syntax: targetSyntax}},
	}
	targetPkg.Files[0].Pkg = targetPkg

	srcSyntax := &ast.File{Name: ast.NewIdent("src")}
	srcPkg := &mast.Package{
		Name:  "src",
		Path:  "example.com/src",
		Files: []*mast.File{{Path: filepath.FromSlash("/proj/src/src.go"), Syntax: srcSyntax}},
	}
	srcPkg.Files[0].Pkg = srcPkg

	ix.Pkgs = []*mast.Package{targetPkg, srcPkg}

	// Verify findPkgForDir finds the package by directory.
	found := findPkgForDir(ix, filepath.FromSlash("/proj/target"))
	if found != targetPkg {
		t.Fatalf("findPkgForDir did not find target package")
	}

	// Verify it returns nil for unknown dirs.
	if findPkgForDir(ix, filepath.FromSlash("/proj/unknown")) != nil {
		t.Fatalf("findPkgForDir should return nil for unknown dir")
	}
}
