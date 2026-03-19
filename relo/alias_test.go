package relo

import (
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

// makeRR builds a resolvedRelo from parsed source, locating the given ident name.
func makeRR(t *testing.T, src, identName string, kind mast.ObjectKind) *resolvedRelo {
	t.Helper()
	file, fset := parseSource(t, src)
	ident := findIdentByName(file, identName)
	if ident == nil {
		t.Fatalf("ident %q not found in source", identName)
	}
	_ = fset
	return &resolvedRelo{
		Group: &mast.Group{
			Name: identName,
			Kind: kind,
		},
		DefIdent: &mast.Ident{
			Ident: ident,
			Kind:  mast.Def,
			File:  &mast.File{Syntax: file},
		},
		File:       &mast.File{Syntax: file},
		TargetName: identName,
	}
}

func TestGenerateFuncAlias_Generic(t *testing.T) {
	src := `package p

func Map[T any](s []T) []T { return nil }
`
	file, fset := parseSource(t, src)
	rr := makeRR(t, src, "Map", mast.Func)
	// Override File.Syntax to use the same parsed file
	rr.File.Syntax = file
	rr.DefIdent.Ident = findIdentByName(file, "Map")

	ar := generateAliases([]*resolvedRelo{rr}, "newpkg", fset)
	if len(ar.Stubs) != 1 {
		t.Fatalf("expected 1 stub, got %d", len(ar.Stubs))
	}
	stub := ar.Stubs[0]

	// The stub must include type parameters in the signature.
	if !strings.Contains(stub, "Map[T any]") {
		t.Errorf("stub missing type params in signature:\n%s", stub)
	}
	// The forwarding call must include type arguments.
	if !strings.Contains(stub, "newpkg.Map[T]") {
		t.Errorf("stub missing type args in forwarding call:\n%s", stub)
	}
}

func TestGenerateFuncAlias_GenericMultiParam(t *testing.T) {
	src := `package p

func Zip[K comparable, V any](keys []K, vals []V) map[K]V { return nil }
`
	file, fset := parseSource(t, src)
	rr := makeRR(t, src, "Zip", mast.Func)
	rr.File.Syntax = file
	rr.DefIdent.Ident = findIdentByName(file, "Zip")

	ar := generateAliases([]*resolvedRelo{rr}, "pkg", fset)
	stub := ar.Stubs[0]

	if !strings.Contains(stub, "Zip[K comparable, V any]") {
		t.Errorf("stub missing multi type params:\n%s", stub)
	}
	if !strings.Contains(stub, "pkg.Zip[K, V]") {
		t.Errorf("stub missing multi type args in call:\n%s", stub)
	}
}

func TestGenerateFuncAlias_NonGeneric(t *testing.T) {
	src := `package p

func Add(a, b int) int { return a + b }
`
	file, fset := parseSource(t, src)
	rr := makeRR(t, src, "Add", mast.Func)
	rr.File.Syntax = file
	rr.DefIdent.Ident = findIdentByName(file, "Add")

	ar := generateAliases([]*resolvedRelo{rr}, "math", fset)
	stub := ar.Stubs[0]

	// Non-generic func should NOT have brackets.
	if strings.Contains(stub, "[") {
		t.Errorf("non-generic stub should not have type params:\n%s", stub)
	}
	if !strings.Contains(stub, "math.Add(a, b)") {
		t.Errorf("unexpected forwarding call:\n%s", stub)
	}
}

func TestGenerateVarAlias_NoGoFixInline(t *testing.T) {
	src := `package p

var ErrFoo = "foo"
`
	file, fset := parseSource(t, src)
	_ = file

	rr := &resolvedRelo{
		Group: &mast.Group{
			Name: "ErrFoo",
			Kind: mast.Var,
		},
		DefIdent: &mast.Ident{
			Ident: findIdentByName(file, "ErrFoo"),
			Kind:  mast.Def,
			File:  &mast.File{Syntax: file},
		},
		File:       &mast.File{Syntax: file},
		TargetName: "ErrFoo",
	}

	ar := generateAliases([]*resolvedRelo{rr}, "newpkg", fset)
	stub := ar.Stubs[0]

	if strings.Contains(stub, "//go:fix inline") {
		t.Errorf("var stub should NOT have //go:fix inline directive:\n%s", stub)
	}
	if !strings.Contains(stub, "// Deprecated:") {
		t.Errorf("var stub should have Deprecated comment:\n%s", stub)
	}
	if !strings.Contains(stub, "var ErrFoo = newpkg.ErrFoo") {
		t.Errorf("var stub has wrong format:\n%s", stub)
	}
}

func TestGenerateFuncAlias_ParamShadowsTargetPkg(t *testing.T) {
	// Parameter "newpkg" shadows the target package name.
	src := `package p

func Send(newpkg string) error { return nil }
`
	file, fset := parseSource(t, src)
	rr := makeRR(t, src, "Send", mast.Func)
	rr.File.Syntax = file
	rr.DefIdent.Ident = findIdentByName(file, "Send")

	ar := generateAliases([]*resolvedRelo{rr}, "newpkg", fset)
	stub := ar.Stubs[0]

	// The forwarding call must use "_newpkg.Send" not bare "newpkg.Send".
	if !strings.Contains(stub, "_newpkg.Send") {
		t.Errorf("stub should use aliased import name _newpkg:\n%s", stub)
	}
	// Make sure the bare (non-aliased) "newpkg.Send" is not used.
	withoutAlias := strings.ReplaceAll(stub, "_newpkg.Send", "")
	if strings.Contains(withoutAlias, "newpkg.Send") {
		t.Errorf("stub uses shadowed package name in forwarding call:\n%s", stub)
	}

	// Should have an import alias.
	if ar.ImportAlias == "" {
		t.Error("expected ImportAlias to be set when param shadows package name")
	}

	// Should have a warning.
	if len(ar.Warnings) == 0 {
		t.Error("expected warning about shadowed package name")
	}
	found := false
	for _, w := range ar.Warnings {
		if strings.Contains(w.Message, "shadows") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning containing 'shadows', got: %v", ar.Warnings)
	}
}

func TestGenerateFuncAlias_NoShadow(t *testing.T) {
	src := `package p

func Send(msg string) error { return nil }
`
	file, fset := parseSource(t, src)
	rr := makeRR(t, src, "Send", mast.Func)
	rr.File.Syntax = file
	rr.DefIdent.Ident = findIdentByName(file, "Send")

	ar := generateAliases([]*resolvedRelo{rr}, "newpkg", fset)

	if ar.ImportAlias != "" {
		t.Errorf("expected no import alias, got %q", ar.ImportAlias)
	}
	if len(ar.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", ar.Warnings)
	}
	if !strings.Contains(ar.Stubs[0], "newpkg.Send") {
		t.Errorf("stub should use original package name:\n%s", ar.Stubs[0])
	}
}
