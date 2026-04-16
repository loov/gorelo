package mast_test

import (
	"go/ast"
	"testing"

	"github.com/loov/gorelo/mast"
)

// TestTypeParamsDoNotCollide verifies that type parameters with the
// same name declared in different generic declarations are tracked as
// distinct groups. Before the TypeName scope fix in objectKeyFor, all
// T-named type parameters in the package merged into one group with
// the package-level `type T` (if any), because *types.TypeName had no
// scope key.
func TestTypeParamsDoNotCollide(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	accumT := firstTypeParamIdent(t, ix, "T", "types.go", "Accumulator")
	shadowT := firstIdentInFunc(t, ix, "T", "scope_extras.go", "TShadow")
	shadowAgainT := firstIdentInFunc(t, ix, "T", "scope_extras.go", "TShadowAgain")

	gAccum := ix.Group(accumT)
	gShadow := ix.Group(shadowT)
	gShadowAgain := ix.Group(shadowAgainT)

	if gAccum == nil || gShadow == nil || gShadowAgain == nil {
		t.Fatalf("missing groups: accum=%v shadow=%v shadowAgain=%v",
			gAccum, gShadow, gShadowAgain)
	}
	if gAccum == gShadow {
		t.Error("Accumulator's T and TShadow's T should be distinct groups")
	}
	if gShadow == gShadowAgain {
		t.Error("TShadow's T and TShadowAgain's T should be distinct groups")
	}
	if gAccum == gShadowAgain {
		t.Error("Accumulator's T and TShadowAgain's T should be distinct groups")
	}
}

func firstIdentInFunc(t *testing.T, ix *mast.Index, name, pathFragment, funcName string) *ast.Ident {
	t.Helper()
	ids := findIdentsInFunc(ix, name, pathFragment, funcName)
	if len(ids) == 0 {
		t.Fatalf("no %q ident in %s", name, funcName)
	}
	return ids[0]
}

// firstTypeParamIdent locates the first ident named `name` declared
// inside the type parameter list of the named generic type.
func firstTypeParamIdent(t *testing.T, ix *mast.Index, name, pathFragment, typeName string) *ast.Ident {
	t.Helper()
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			if !pathContains(file.Path, pathFragment) {
				continue
			}
			for _, decl := range file.Syntax.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name != typeName || ts.TypeParams == nil {
						continue
					}
					for _, field := range ts.TypeParams.List {
						for _, id := range field.Names {
							if id.Name == name {
								return id
							}
						}
					}
				}
			}
		}
	}
	t.Fatalf("no type param %q in %s", name, typeName)
	return nil
}
