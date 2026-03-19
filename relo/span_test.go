package relo

import (
	"go/ast"
	"go/parser"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestFindEnclosingDecl(t *testing.T) {
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
	file, _ := parseSource(t, "package p\nvar X = 1\n")
	fake := &ast.Ident{Name: "fake"}
	decl := findEnclosingDecl(file, fake)
	if decl != nil {
		t.Errorf("expected nil for unknown ident, got %T", decl)
	}
}

func TestFindSpecForIdent(t *testing.T) {
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

	pkgDir := dirOf(ix.Pkgs[0].Files[0].Path)
	rr := &resolvedRelo{
		Group:      grp,
		DefIdent:   defIdent,
		File:       defIdent.File,
		TargetFile: joinPath(pkgDir, "target.go"),
		TargetName: "X",
	}

	plan := &Plan{}
	computeSpans(ix, []*resolvedRelo{rr}, plan)

	if !hasWarning(plan, "multi-name declaration") {
		t.Errorf("expected multi-name warning, got: %v", plan.Warnings)
	}
}

func TestDedentBlock(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single level",
			input: "\tA = 1\n\tB = 2",
			want:  "A = 1\nB = 2",
		},
		{
			name:  "two levels removes one",
			input: "\t\tdeep\n\t\tindent",
			want:  "\tdeep\n\tindent",
		},
		{
			name:  "no tabs",
			input: "no indent",
			want:  "no indent",
		},
		{
			name:  "mixed",
			input: "\tindented\nnot indented",
			want:  "indented\nnot indented",
		},
		{
			name:  "empty",
			input: "",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedentBlock(tt.input)
			if got != tt.want {
				t.Errorf("dedentBlock(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPrependKeyword(t *testing.T) {
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
			got := prependKeyword(tt.text, tt.keyword)
			if got != tt.want {
				t.Errorf("prependKeyword(%q, %q) = %q, want %q", tt.text, tt.keyword, got, tt.want)
			}
		})
	}
}
