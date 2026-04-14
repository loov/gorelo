package mast_test

import (
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestIsPackageScope_PackageLevelFunc(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// NewUser is a package-level function.
	ids := findIdentsInFile(ix, "NewUser", "funcs.go")
	if len(ids) == 0 {
		t.Fatal("no NewUser idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("NewUser has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("NewUser should be package-scope")
	}
}

func TestIsPackageScope_PackageLevelVar(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFile(ix, "DefaultUser", "vars.go")
	if len(ids) == 0 {
		t.Fatal("no DefaultUser idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("DefaultUser has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("DefaultUser should be package-scope")
	}
}

func TestIsPackageScope_PackageLevelConst(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFile(ix, "MaxUsers", "vars.go")
	if len(ids) == 0 {
		t.Fatal("no MaxUsers idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("MaxUsers has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("MaxUsers should be package-scope")
	}
}

func TestIsPackageScope_PackageLevelType(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFile(ix, "Counter", "types.go")
	if len(ids) == 0 {
		t.Fatal("no Counter idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("Counter has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("Counter should be package-scope")
	}
}

func TestIsPackageScope_IotaConst(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFile(ix, "RoleAdmin", "vars.go")
	if len(ids) == 0 {
		t.Fatal("no RoleAdmin idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("RoleAdmin has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("RoleAdmin should be package-scope")
	}
}

func TestIsPackageScope_PackageLevelError(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFile(ix, "ErrNotFound", "vars.go")
	if len(ids) == 0 {
		t.Fatal("no ErrNotFound idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("ErrNotFound has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("ErrNotFound should be package-scope")
	}
}

func TestIsPackageScope_FuncParameter(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// "name" as a parameter in ValidateUser.
	ids := findIdentsInFunc(ix, "name", "scoping.go", "ValidateUser")
	if len(ids) == 0 {
		t.Fatal("no name idents found in ValidateUser")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("parameter name has no group")
	}
	if grp.IsPackageScope() {
		t.Error("parameter 'name' in ValidateUser should NOT be package-scope")
	}
}

func TestIsPackageScope_LocalVar(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// "ok" as a local variable in ValidateUser.
	ids := findIdentsInFunc(ix, "ok", "scoping.go", "ValidateUser")
	if len(ids) == 0 {
		t.Fatal("no ok idents found in ValidateUser")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("local ok has no group")
	}
	if grp.IsPackageScope() {
		t.Error("local 'ok' in ValidateUser should NOT be package-scope")
	}
}

func TestIsPackageScope_FuncLitParamInGenDecl(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// "page" is a parameter of a function literal assigned to the
	// package-level var LogFn. It lives inside a GenDecl, not a
	// FuncDecl, so the naive FuncDecl-only check used to misreport
	// it as package-scope.
	ids := findIdentsInFile(ix, "page", "closures.go")
	if len(ids) == 0 {
		t.Fatal("no page idents found in closures.go")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("func-literal param 'page' has no group")
	}
	if grp.IsPackageScope() {
		t.Error("func-literal param 'page' should NOT be package-scope")
	}
}

func TestIsPackageScope_ShortVarDecl(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// "count" is a short var decl in MakeCounter.
	ids := findIdentsInFunc(ix, "count", "closures.go", "MakeCounter")
	if len(ids) == 0 {
		t.Fatal("no count idents found in MakeCounter")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("local count has no group")
	}
	if grp.IsPackageScope() {
		t.Error("local 'count' in MakeCounter should NOT be package-scope")
	}
}

func TestIsPackageScope_ReceiverVar(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// "u" is a receiver parameter in Rename.
	ids := findIdentsInFunc(ix, "u", "scoping.go", "Rename")
	if len(ids) == 0 {
		t.Fatal("no u idents found in Rename")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("receiver u has no group")
	}
	if grp.IsPackageScope() {
		t.Error("receiver 'u' in Rename should NOT be package-scope")
	}
}

func TestIsPackageScope_LocalConst(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// "limit" is a local const in LocalConst.
	ids := findIdentsInFunc(ix, "limit", "scoping.go", "LocalConst")
	if len(ids) == 0 {
		t.Fatal("no limit idents found in LocalConst")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("local limit has no group")
	}
	if grp.IsPackageScope() {
		t.Error("local const 'limit' in LocalConst should NOT be package-scope")
	}
}

func TestIsPackageScope_Method(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// String is a method on User — the method name ident is package-scope
	// (it's the FuncDecl.Name, which is the defining ident at top level).
	ids := findIdentsInFile(ix, "String", "funcs.go")
	if len(ids) == 0 {
		t.Fatal("no String idents found in funcs.go")
	}
	// Find the one that's a method def (inside a FuncDecl with receiver).
	var methodGroup *mast.Group
	for _, id := range ids {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Method {
			methodGroup = grp
			break
		}
	}
	if methodGroup == nil {
		t.Fatal("no Method group found for String")
	}
	if !methodGroup.IsPackageScope() {
		t.Error("method String should be package-scope")
	}
}

func TestIsPackageScope_ResultVar(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	// In ShortVarDeclReuse, "err" is a result parameter.
	// Find the err def in that function.
	ids := findIdentsInFunc(ix, "err", "scoping.go", "ShortVarDeclReuse")
	if len(ids) == 0 {
		t.Fatal("no err idents in ShortVarDeclReuse")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("err has no group")
	}
	if grp.IsPackageScope() {
		t.Error("result 'err' in ShortVarDeclReuse should NOT be package-scope")
	}
}

func TestIsPackageScope_GenericFunc(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFile(ix, "MakePair", "funcs.go")
	if len(ids) == 0 {
		t.Fatal("no MakePair idents found")
	}
	grp := ix.Group(ids[0])
	if grp == nil {
		t.Fatal("MakePair has no group")
	}
	if !grp.IsPackageScope() {
		t.Error("MakePair should be package-scope")
	}
}
