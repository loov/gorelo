package mast_test

import (
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestLocalVariableScoping(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// "name" is a parameter in LookupUser and ValidateUser — separate groups.
	lookupNames := findIdentsInFunc(ix, "name", "expressions.go", "LookupUser")
	validateNames := findIdentsInFunc(ix, "name", "scoping.go", "ValidateUser")
	if len(lookupNames) == 0 || len(validateNames) == 0 {
		t.Fatal("missing name idents in LookupUser or ValidateUser")
	}

	g1 := ix.Group(lookupNames[0])
	g2 := ix.Group(validateNames[0])
	if g1 == nil || g2 == nil {
		t.Fatal("name parameter has no group")
	}
	if g1 == g2 {
		t.Error("'name' in LookupUser and ValidateUser should be in separate groups")
	}
}

func TestLocalOkVariableScoping(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	lookupOk := findIdentsInFunc(ix, "ok", "expressions.go", "LookupUser")
	validateOk := findIdentsInFunc(ix, "ok", "scoping.go", "ValidateUser")
	if len(lookupOk) == 0 || len(validateOk) == 0 {
		t.Fatal("missing ok idents")
	}

	g1 := ix.Group(lookupOk[0])
	g2 := ix.Group(validateOk[0])
	if g1 == nil || g2 == nil {
		t.Fatal("ok variable has no group")
	}
	if g1 == g2 {
		t.Error("'ok' in LookupUser and ValidateUser should be in separate groups")
	}
}

func TestLocalShadowingPackageVar(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	pkgIdents := findIdentsInFile(ix, "DefaultUser", "vars.go")
	if len(pkgIdents) == 0 {
		t.Fatal("no DefaultUser idents in vars.go")
	}
	pkgGrp := ix.Group(pkgIdents[0])
	if pkgGrp == nil {
		t.Fatal("package-level DefaultUser has no group")
	}

	// The local shadow in ShadowDefaultUser should be in a different group.
	localIdents := findIdentsInFunc(ix, "DefaultUser", "scoping.go", "ShadowDefaultUser")
	if len(localIdents) == 0 {
		t.Fatal("no DefaultUser idents in ShadowDefaultUser")
	}
	localGrp := ix.Group(localIdents[0])
	if localGrp == pkgGrp {
		t.Error("local shadow of DefaultUser should be in separate group from package var")
	}
}

func TestNestedScopeVariable(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	errIdents := findIdentsInFunc(ix, "err", "scoping.go", "NestedScopeErr")
	if len(errIdents) == 0 {
		t.Fatal("no err idents in NestedScopeErr")
	}

	errGroups := map[*mast.Group]bool{}
	for _, id := range errIdents {
		if g := ix.Group(id); g != nil {
			errGroups[g] = true
		}
	}
	if len(errGroups) < 2 {
		t.Errorf("expected outer and inner 'err' in NestedScopeErr to be in separate groups, got %d", len(errGroups))
	}
}

func TestShortVarDeclReuse(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// In ShortVarDeclReuse, "err" is introduced then reused — same group.
	errIdents := findIdentsInFunc(ix, "err", "scoping.go", "ShortVarDeclReuse")
	if len(errIdents) == 0 {
		t.Fatal("no err idents in ShortVarDeclReuse")
	}

	grp := ix.Group(errIdents[0])
	if grp == nil {
		t.Fatal("err in ShortVarDeclReuse has no group")
	}
	for _, id := range errIdents {
		if ix.Group(id) != grp {
			t.Errorf("err at %v should be in same group within ShortVarDeclReuse",
				ix.Fset.Position(id.Pos()))
		}
	}
}

func TestReceiverVariable(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// "u" receiver in different methods should be separate groups.
	funcU := findIdentsInFile(ix, "u", "funcs.go")
	scopingU := findIdentsInFunc(ix, "u", "scoping.go", "Rename")
	if len(funcU) == 0 || len(scopingU) == 0 {
		t.Skip("receiver variable idents not found in both files")
	}

	funcUGroups := map[*mast.Group]bool{}
	for _, id := range funcU {
		if g := ix.Group(id); g != nil {
			funcUGroups[g] = true
		}
	}

	for _, id := range scopingU {
		if g := ix.Group(id); g != nil && funcUGroups[g] {
			t.Error("receiver 'u' in funcs.go and scoping.go should be in separate groups")
			break
		}
	}
}

func TestClosureCapture(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// count in MakeCounter: def + closure uses should be same group.
	makeCounterCount := findIdentsInFunc(ix, "count", "closures.go", "MakeCounter")
	if len(makeCounterCount) == 0 {
		t.Fatal("no count idents in MakeCounter")
	}

	grp := ix.Group(makeCounterCount[0])
	if grp == nil {
		t.Fatal("count in MakeCounter has no group")
	}

	for _, id := range makeCounterCount {
		if ix.Group(id) != grp {
			t.Errorf("count at %v not in same group (closure capture broken)",
				ix.Fset.Position(id.Pos()))
		}
	}
	if len(grp.Idents) < 3 {
		t.Errorf("expected at least 3 count idents in MakeCounter (def + closure uses), got %d", len(grp.Idents))
	}

	// count in CountUsers should be in a different group.
	countUsersCount := findIdentsInFunc(ix, "count", "scoping.go", "CountUsers")
	for _, id := range countUsersCount {
		if ix.Group(id) == grp {
			t.Error("count in CountUsers should be in separate group from count in MakeCounter")
			break
		}
	}
}

func TestClosureSameNamedParam(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// TransformAll has outer param "x" and inner closure param "x" — separate groups.
	xIdents := findIdentsInFunc(ix, "x", "closures.go", "TransformAll")

	xGroups := map[*mast.Group]bool{}
	for _, id := range xIdents {
		if g := ix.Group(id); g != nil {
			xGroups[g] = true
		}
	}
	if len(xGroups) < 2 {
		t.Errorf("expected outer and inner 'x' in TransformAll to be in separate groups, got %d", len(xGroups))
	}
}

func TestBlankIdentifierUntracked(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	for _, file := range []string{"scoping.go", "expressions.go"} {
		blankIdents := findIdentsInFile(ix, "_", file)
		for _, id := range blankIdents {
			if grp := ix.Group(id); grp != nil {
				t.Errorf("blank identifier at %v should be untracked but has group %q",
					ix.Fset.Position(id.Pos()), grp.Name)
			}
		}
	}
}

func TestPackageNameUntracked(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			pkgIdent := file.Syntax.Name
			if grp := ix.Group(pkgIdent); grp != nil {
				t.Errorf("package name ident %q at %v should be untracked",
					pkgIdent.Name, ix.Fset.Position(pkgIdent.Pos()))
			}
		}
	}
}

func TestMultipleInitFunctions(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	initIdents := findIdentsInFile(ix, "init", "scoping.go")
	if len(initIdents) == 0 {
		t.Fatal("no init idents in scoping.go")
	}

	initGroups := map[*mast.Group]bool{}
	for _, id := range initIdents {
		if g := ix.Group(id); g != nil {
			initGroups[g] = true
		}
	}
	if len(initGroups) < 2 {
		t.Errorf("expected multiple init() functions to be in separate groups, got %d", len(initGroups))
	}
}

func TestLocalConst(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	limitIdents := findIdentsInFunc(ix, "limit", "scoping.go", "LocalConst")
	if len(limitIdents) == 0 {
		t.Fatal("no limit idents in LocalConst")
	}

	grp := ix.Group(limitIdents[0])
	if grp == nil {
		t.Fatal("local const limit has no group")
	}
	if grp.Kind != mast.Const {
		t.Errorf("expected Const kind for limit, got %v", grp.Kind)
	}
	for _, id := range limitIdents {
		if ix.Group(id) != grp {
			t.Error("limit idents not all in same group")
			break
		}
	}
}

func TestRenamedImport(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	lnxIdents := findIdentsInFile(ix, "lnx", "imports.go")
	if len(lnxIdents) == 0 {
		t.Fatal("no lnx idents in imports.go")
	}

	var pkgNameGrp *mast.Group
	for _, id := range lnxIdents {
		if g := ix.Group(id); g != nil {
			pkgNameGrp = g
			break
		}
	}
	if pkgNameGrp == nil {
		t.Fatal("lnx import has no group")
	}
	if pkgNameGrp.Kind != mast.PackageName {
		t.Errorf("expected PackageName kind for lnx, got %v", pkgNameGrp.Kind)
	}
	for _, id := range lnxIdents {
		if ix.Group(id) != pkgNameGrp {
			t.Error("lnx idents not all in same group")
			break
		}
	}
}

func TestSelectCaseVariableScoping(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// SelectFirst has "e" in two different select cases — each is a new variable.
	eIdents := findIdentsInFunc(ix, "e", "channels.go", "SelectFirst")
	if len(eIdents) == 0 {
		t.Fatal("no e idents in SelectFirst")
	}

	eGroups := map[*mast.Group]bool{}
	for _, id := range eIdents {
		if g := ix.Group(id); g != nil {
			eGroups[g] = true
		}
	}
	if len(eGroups) < 2 {
		t.Errorf("expected 'e' in different select cases to be in separate groups, got %d", len(eGroups))
	}
}

func TestDotImport(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// dotimport.go uses `import . "example/linux"`.
	// Name() called without qualifier should link to linux.Name.
	linuxNameGrp := findFuncGroup(ix, "Name", "linux/linux.go")
	if linuxNameGrp == nil {
		t.Fatal("no Func group for linux.Name")
	}

	found := false
	for _, id := range findIdentsInFunc(ix, "Name", "dotimport.go", "DotImportName") {
		if ix.Group(id) == linuxNameGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("Name() via dot import not linked to linux.Name function group")
	}

	// Info type via dot import should link to linux.Info.
	infoGrp := findTypeGroup(ix, "Info", "linux/linux.go")
	if infoGrp == nil {
		t.Fatal("no TypeName group for linux.Info")
	}

	found = false
	for _, id := range findIdentsInFile(ix, "Info", "dotimport.go") {
		if ix.Group(id) == infoGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("Info type via dot import not linked to linux.Info type group")
	}
}

func TestSideEffectImport(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// sideeffect.go uses `import _ "example/linux"`.
	// The file should be loaded and SideEffectVar should have a group.
	var fileFound bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "sideeffect.go") {
				fileFound = true
			}
		}
	}
	if !fileFound {
		t.Fatal("sideeffect.go not loaded")
	}

	idents := findIdentsInFile(ix, "SideEffectVar", "sideeffect.go")
	if len(idents) == 0 {
		t.Fatal("no SideEffectVar idents")
	}
	if ix.Group(idents[0]) == nil {
		t.Error("SideEffectVar has no group")
	}

	// The "_" in `import _ "example/linux"` should be untracked.
	blankIdents := findIdentsInFile(ix, "_", "sideeffect.go")
	for _, id := range blankIdents {
		if g := ix.Group(id); g != nil {
			t.Errorf("blank import ident at %v should be untracked", ix.Fset.Position(id.Pos()))
		}
	}
}

func TestImportAliasCollisionAcrossFiles(t *testing.T) {
	t.Parallel()

	ix := loadTestdata(t)

	// imports.go has `import lnx "example/linux"` (linux-tagged).
	// imports2.go has `import lnx "example/windows"` (windows-tagged).
	// These are different imports with the same alias. They should be
	// in separate groups — renaming lnx in one file shouldn't affect the other.
	lnxInImports := findIdentsInFile(ix, "lnx", "imports.go")
	lnxInImports2 := findIdentsInFile(ix, "lnx", "imports2.go")

	if len(lnxInImports) == 0 {
		t.Fatal("no lnx idents in imports.go")
	}
	if len(lnxInImports2) == 0 {
		t.Fatal("no lnx idents in imports2.go")
	}

	grp1 := ix.Group(lnxInImports[0])
	grp2 := ix.Group(lnxInImports2[0])
	if grp1 == nil || grp2 == nil {
		t.Fatal("lnx import has no group")
	}
	if grp1 == grp2 {
		t.Error("lnx in imports.go and imports2.go point to different packages and should be in separate groups")
	}
}
