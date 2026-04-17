package relo

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestFindEnclosingDecl(t *testing.T) {
	t.Parallel()

	src := `package p

type Foo struct{}

func Bar() {}

var X = 1
`
	file, _ := parseSource(t, src)

	tests := []struct {
		name      string
		identName string
		wantKind  string // "FuncDecl" or "GenDecl"
	}{
		{"type", "Foo", "GenDecl"},
		{"func", "Bar", "FuncDecl"},
		{"var", "X", "GenDecl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ident := findIdentByName(file, tt.identName)
			if ident == nil {
				t.Fatalf("ident %q not found", tt.identName)
			}
			decl := findEnclosingDecl(file, ident)
			if decl == nil {
				t.Fatalf("no enclosing decl for %q", tt.identName)
			}
			switch decl.(type) {
			case *ast.FuncDecl:
				if tt.wantKind != "FuncDecl" {
					t.Errorf("got FuncDecl, want %s", tt.wantKind)
				}
			case *ast.GenDecl:
				if tt.wantKind != "GenDecl" {
					t.Errorf("got GenDecl, want %s", tt.wantKind)
				}
			}
		})
	}
}

func TestFindEnclosingDecl_NotFound(t *testing.T) {
	t.Parallel()

	file, _ := parseSource(t, "package p\nvar X = 1\n")
	fake := &ast.Ident{Name: "fake"}
	decl := findEnclosingDecl(file, fake)
	if decl != nil {
		t.Errorf("expected nil for unknown ident, got %T", decl)
	}
}

func TestFindSpecForIdent(t *testing.T) {
	t.Parallel()

	src := `package p

const (
	A = 1
	B = 2
	C = 3
)
`
	file, _ := parseSource(t, src)
	gd := file.Decls[0].(*ast.GenDecl)

	identB := findIdentByName(file, "B")
	spec := findSpecForIdent(gd, identB)
	if spec == nil {
		t.Fatal("expected to find spec for B")
	}
	vs, ok := spec.(*ast.ValueSpec)
	if !ok {
		t.Fatalf("expected *ast.ValueSpec, got %T", spec)
	}
	if vs.Names[0].Name != "B" {
		t.Errorf("expected spec for B, got %s", vs.Names[0].Name)
	}

	// Not found case.
	fake := &ast.Ident{Name: "Z"}
	if findSpecForIdent(gd, fake) != nil {
		t.Error("expected nil for ident not in GenDecl")
	}
}

func TestExprListUsesIota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want bool
	}{
		{"iota", true},
		{"iota + 1", true},
		{"1 << iota", true},
		{"42", false},
		{`"hello"`, false},
		{"x + y", false},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()

			expr, err := parser.ParseExpr(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			got := exprListUsesIota([]ast.Expr{expr})
			if got != tt.want {
				t.Errorf("exprListUsesIota(%q) = %v, want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestConstSpecDependsOnIota(t *testing.T) {
	t.Parallel()

	src := `package p

const (
	A = iota
	B
	C = 100
	D
)
`
	file, _ := parseSource(t, src)
	gd := file.Decls[0].(*ast.GenDecl)

	tests := []struct {
		name string
		want bool
	}{
		{"A", true},  // has iota directly
		{"B", true},  // inherits from A which uses iota
		{"C", false}, // explicit 100
		{"D", false}, // inherits from C which is 100
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if vs.Names[0].Name == tt.name {
					got := constSpecDependsOnIota(gd, vs)
					if got != tt.want {
						t.Errorf("constSpecDependsOnIota(%s) = %v, want %v", tt.name, got, tt.want)
					}
					return
				}
			}
			t.Fatalf("spec %q not found", tt.name)
		})
	}
}

func TestComputeSpans_MultiNameValueSpecWarning(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": "package p\n\nvar X, Y = 1, 2\n",
	})

	ident := findDefIdentInIndex(ix, "X")
	if ident == nil {
		t.Fatal("var X not found")
	}

	grp := ix.Group(ident)
	var defIdent *mast.Ident
	for _, id := range grp.Idents {
		if id.Kind == mast.Def {
			defIdent = id
			break
		}
	}

	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	rr := &resolvedRelo{
		Group:      grp,
		DefIdent:   defIdent,
		File:       defIdent.File,
		TargetFile: filepath.Join(pkgDir, "target.go"),
		TargetName: "X",
	}

	plan := &Plan{}
	computeSpans(ix, []*resolvedRelo{rr}, plan)

	if !hasWarning(plan, "multi-name declaration") {
		t.Errorf("expected multi-name warning, got: %v", plan.Warnings)
	}
}

func TestCheckIotaBlock_DifferentTargets(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": `package p

const (
	A = iota
	B
	C
)
`,
	})

	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)

	// Build a resolvedRelo for each const in the iota block, sending A and B
	// to different target files.
	names := []string{"A", "B", "C"}
	targets := []string{
		filepath.Join(pkgDir, "target1.go"),
		filepath.Join(pkgDir, "target2.go"), // different target
		filepath.Join(pkgDir, "target1.go"),
	}

	var resolved []*resolvedRelo
	for i, name := range names {
		ident := findDefIdentInIndex(ix, name)
		if ident == nil {
			t.Fatalf("ident %q not found", name)
		}
		grp := ix.Group(ident)
		var defIdent *mast.Ident
		for _, id := range grp.Idents {
			if id.Kind == mast.Def {
				defIdent = id
				break
			}
		}
		resolved = append(resolved, &resolvedRelo{
			Group:      grp,
			DefIdent:   defIdent,
			File:       defIdent.File,
			TargetFile: targets[i],
			TargetName: name,
		})
	}

	plan := &Plan{}
	_, err := computeSpans(ix, resolved, plan)
	if err == nil {
		t.Fatal("expected error for iota block with different targets, got nil")
	}
	if !errContains(err, "same target file") {
		t.Errorf("expected error about same target file, got: %v", err)
	}
}

func TestCheckIotaBlock_SameTarget(t *testing.T) {
	t.Parallel()

	ix := loadTestIndex(t, map[string]string{
		"main.go": `package p

const (
	A = iota
	B
	C
)
`,
	})

	pkgDir := filepath.Dir(ix.Pkgs[0].Files[0].Path)
	target := filepath.Join(pkgDir, "target.go")

	var resolved []*resolvedRelo
	for _, name := range []string{"A", "B", "C"} {
		ident := findDefIdentInIndex(ix, name)
		if ident == nil {
			t.Fatalf("ident %q not found", name)
		}
		grp := ix.Group(ident)
		var defIdent *mast.Ident
		for _, id := range grp.Idents {
			if id.Kind == mast.Def {
				defIdent = id
				break
			}
		}
		resolved = append(resolved, &resolvedRelo{
			Group:      grp,
			DefIdent:   defIdent,
			File:       defIdent.File,
			TargetFile: target,
			TargetName: name,
		})
	}

	plan := &Plan{}
	_, err := computeSpans(ix, resolved, plan)
	if err != nil {
		t.Fatalf("expected no error when all iota specs go to same target, got: %v", err)
	}
}

func TestSpecByteRange_AdjacentSpecs_NoBoundaryOverlap(t *testing.T) {
	t.Parallel()

	src := "package p\n\nconst (\n\tA = 1\n\tB = 2\n\tC = 3\n)\n"
	ix := loadTestIndex(t, map[string]string{
		"main.go": src,
	})

	file := ix.Pkgs[0].Files[0]
	gd := file.Syntax.Decls[0].(*ast.GenDecl)

	// Compute ranges for all three specs.
	type specRange struct {
		name       string
		start, end int
	}
	var ranges []specRange
	for _, spec := range gd.Specs {
		vs := spec.(*ast.ValueSpec)
		start, end := specByteRange(ix.Fset, spec, file)
		ranges = append(ranges, specRange{
			name:  vs.Names[0].Name,
			start: start,
			end:   end,
		})
	}

	// Verify no overlap: each range's start should be >= the previous range's end.
	for i := 1; i < len(ranges); i++ {
		prev := ranges[i-1]
		curr := ranges[i]
		if curr.start < prev.end {
			t.Errorf("overlap between %s [%d,%d) and %s [%d,%d)",
				prev.name, prev.start, prev.end,
				curr.name, curr.start, curr.end)
		}
	}

	// Each spec's text should contain exactly one trailing newline (not more).
	content := string(fileContent(file))
	for _, r := range ranges {
		text := content[r.start:r.end]
		trimmed := strings.TrimRight(text, "\n")
		nlCount := len(text) - len(trimmed)
		if nlCount != 1 {
			t.Errorf("spec %s text %q has %d trailing newlines, want 1",
				r.name, text, nlCount)
		}
	}
}

func TestSpecByteRange_BlankLineBetweenSpecs(t *testing.T) {
	t.Parallel()

	src := "package p\n\nconst (\n\tA = 1\n\n\tB = 2\n)\n"
	ix := loadTestIndex(t, map[string]string{
		"main.go": src,
	})

	file := ix.Pkgs[0].Files[0]
	gd := file.Syntax.Decls[0].(*ast.GenDecl)
	content := string(fileContent(file))

	specA := gd.Specs[0]
	specB := gd.Specs[1]

	startA, endA := specByteRange(ix.Fset, specA, file)
	startB, endB := specByteRange(ix.Fset, specB, file)

	// A should claim only one trailing newline, not the blank line.
	textA := content[startA:endA]
	if strings.HasSuffix(textA, "\n\n") {
		t.Errorf("spec A should not claim multiple trailing newlines, got %q", textA)
	}

	// B should not overlap with A.
	if startB < endA {
		t.Errorf("spec B [%d,%d) overlaps with spec A [%d,%d)", startB, endB, startA, endA)
	}
}

func TestPrependKeyword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		keyword string
		want    string
	}{
		{
			name:    "simple",
			text:    "Foo = 1",
			keyword: "const",
			want:    "const Foo = 1",
		},
		{
			name:    "with doc comment",
			text:    "// Doc\nFoo = 1",
			keyword: "var",
			want:    "// Doc\nvar Foo = 1",
		},
		{
			name:    "blank line then code",
			text:    "\n// Doc\nFoo int",
			keyword: "type",
			want:    "\n// Doc\ntype Foo int",
		},
		{
			name:    "all comments",
			text:    "// only comment",
			keyword: "const",
			want:    "const // only comment",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prependKeyword(tt.text, tt.keyword)
			if got != tt.want {
				t.Errorf("prependKeyword(%q, %q) = %q, want %q", tt.text, tt.keyword, got, tt.want)
			}
		})
	}
}
