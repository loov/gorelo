package mast_test

import (
	"strings"
	"testing"

	"github.com/loov/mast"
)

func TestTypeRename(t *testing.T) {
	ix := loadTestdata(t)

	idents := findIdents(ix, "Counter")
	if len(idents) == 0 {
		t.Fatal("no Counter idents found")
	}

	grp := ix.Group(idents[0])
	if grp == nil {
		t.Fatal("Counter ident has no group")
	}

	for _, id := range idents {
		if ix.Group(id) != grp {
			t.Errorf("Counter ident at %v in different group", ix.Fset.Position(id.Pos()))
		}
	}

	if grp.Kind != mast.TypeName {
		t.Errorf("expected TypeName kind, got %v", grp.Kind)
	}
}

func TestFieldRename(t *testing.T) {
	ix := loadTestdata(t)

	grp := findFieldGroup(ix, "Name", "structs.go")
	if grp == nil {
		t.Fatal("no Field group found for Name")
	}
	if len(grp.Idents) < 2 {
		t.Errorf("expected at least 2 idents in Name field group, got %d", len(grp.Idents))
	}
}

func TestSameNameFieldsDifferentStructs(t *testing.T) {
	ix := loadTestdata(t)

	userNameGrp := findFieldGroup(ix, "Name", "structs.go")
	if userNameGrp == nil {
		t.Fatal("no Field group for User.Name in structs.go")
	}

	fileNameGrp := findFieldGroup(ix, "Name", "platform_linux.go")
	if fileNameGrp == nil {
		fileNameGrp = findFieldGroup(ix, "Name", "platform_windows.go")
	}
	if fileNameGrp == nil {
		t.Fatal("no Field group for File.Name in platform files")
	}

	if userNameGrp == fileNameGrp {
		t.Error("User.Name and File.Name must be in separate groups")
	}

	// f.Name in platform_common.go should be in File.Name's group.
	for _, id := range findIdentsInFile(ix, "Name", "platform_common.go") {
		if ix.Group(id) == userNameGrp {
			t.Errorf("Name at %v is in User.Name group but should be in File.Name group",
				ix.Fset.Position(id.Pos()))
		}
	}
}

func TestEmbeddedField(t *testing.T) {
	ix := loadTestdata(t)

	typeGroup := findTypeGroup(ix, "User", "structs.go")
	if typeGroup == nil {
		t.Fatal("no TypeName group for User")
	}
	if len(typeGroup.Idents) < 2 {
		t.Errorf("expected User type group to have at least 2 idents (def + embedded), got %d", len(typeGroup.Idents))
	}

	hasEmbedded := false
	for _, id := range typeGroup.Idents {
		if strings.Contains(id.File.Path, "structs.go") && id.Kind == mast.Use {
			hasEmbedded = true
		}
	}
	if !hasEmbedded {
		t.Error("embedded User field in structs.go not linked to User type group")
	}
}

func TestPromotedFieldMultiLevel(t *testing.T) {
	ix := loadTestdata(t)

	userNameGrp := findFieldGroup(ix, "Name", "structs.go")
	if userNameGrp == nil {
		t.Fatal("no User.Name field group found")
	}

	// sa.Name in SuperAdminName should resolve to User.Name.
	found := false
	for _, id := range findIdentsInFunc(ix, "Name", "structs.go", "SuperAdminName") {
		if ix.Group(id) == userNameGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("sa.Name in SuperAdminName not linked to User.Name field group")
	}
}

func TestFieldThroughTypeAlias(t *testing.T) {
	ix := loadTestdata(t)

	userNameGrp := findFieldGroup(ix, "Name", "structs.go")
	if userNameGrp == nil {
		t.Fatal("no Field group for User.Name in structs.go")
	}

	// m.Name in MemberName (via type alias Member) should be in User.Name group.
	found := false
	for _, id := range findIdentsInFunc(ix, "Name", "structs.go", "MemberName") {
		if ix.Group(id) == userNameGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("m.Name in MemberName (via type alias) not linked to User.Name field group")
	}
}

func TestSelectorOnReturnValue(t *testing.T) {
	ix := loadTestdata(t)

	userNameGrp := findFieldGroup(ix, "Name", "structs.go")
	if userNameGrp == nil {
		t.Fatal("no User.Name field group found")
	}

	// NewUser(...).Name in DefaultUserName should resolve to User.Name.
	found := false
	for _, id := range findIdentsInFunc(ix, "Name", "structs.go", "DefaultUserName") {
		if ix.Group(id) == userNameGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("NewUser(...).Name selector not linked to User.Name field group")
	}
}

func TestAnonymousStructFields(t *testing.T) {
	ix := loadTestdata(t)

	grp := findFieldGroup(ix, "Host", "structs.go")
	if grp == nil {
		t.Fatal("Host field has no group")
	}
	if grp.Kind != mast.Field {
		t.Errorf("expected Field kind for Host, got %v", grp.Kind)
	}
	if len(grp.Idents) < 2 {
		t.Errorf("expected at least 2 Host field idents, got %d", len(grp.Idents))
	}
}

func TestCrossPackageFieldAccess(t *testing.T) {
	ix := loadTestdata(t)

	distroGrp := findFieldGroup(ix, "Distro", "linux/linux.go")
	if distroGrp == nil {
		t.Fatal("no Field group for Distro in linux/linux.go")
	}

	found := false
	for _, id := range findIdentsInFile(ix, "Distro", "platform_linux.go") {
		if ix.Group(id) == distroGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("info.Distro in platform_linux.go not linked to linux.Info.Distro field group")
	}
}

func TestMethodRename(t *testing.T) {
	ix := loadTestdata(t)

	grp := findMethodGroup(ix, "String", "funcs.go")
	if grp == nil {
		t.Fatal("no Method group for String")
	}
	if grp.Name != "String" {
		t.Errorf("expected group name String, got %s", grp.Name)
	}
}

func TestSameMethodNameDifferentTypes(t *testing.T) {
	ix := loadTestdata(t)

	userStringGrp := findMethodGroup(ix, "String", "funcs.go")
	serverStringGrp := findMethodGroup(ix, "String", "structs.go")

	if userStringGrp == nil {
		t.Fatal("no Method group for User.String")
	}
	if serverStringGrp == nil {
		t.Fatal("no Method group for Server.String")
	}
	if userStringGrp == serverStringGrp {
		t.Error("User.String() and Server.String() must be in separate groups")
	}
}

func TestMethodExpression(t *testing.T) {
	ix := loadTestdata(t)

	userStringGrp := findMethodGroup(ix, "String", "funcs.go")
	if userStringGrp == nil {
		t.Fatal("no Method group for User.String in funcs.go")
	}

	found := false
	for _, id := range findIdentsInFunc(ix, "String", "expressions.go", "MethodExpr") {
		if ix.Group(id) == userStringGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("User.String method expression not linked to User.String method group")
	}
}

func TestPointerMethodExpression(t *testing.T) {
	ix := loadTestdata(t)

	setEmailGrp := findMethodGroup(ix, "SetEmail", "funcs.go")
	if setEmailGrp == nil {
		t.Fatal("no Method group for SetEmail in funcs.go")
	}

	found := false
	for _, id := range findIdentsInFunc(ix, "SetEmail", "expressions.go", "PointerMethodExpr") {
		if ix.Group(id) == setEmailGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("(*User).SetEmail method expression not linked to SetEmail method group")
	}
}

func TestGenericTypeParams(t *testing.T) {
	ix := loadTestdata(t)

	aIdents := findIdentsInFile(ix, "A", "types.go")
	if len(aIdents) == 0 {
		t.Fatal("no A idents in types.go")
	}

	grp := ix.Group(aIdents[0])
	if grp == nil {
		t.Fatal("type param A has no group")
	}
	for _, id := range aIdents {
		if ix.Group(id) != grp {
			t.Error("type param A idents not all in same group")
			break
		}
	}
}

func TestGenericInstantiationWithNamedTypes(t *testing.T) {
	ix := loadTestdata(t)

	counterGrp := findTypeGroup(ix, "Counter", "types.go")
	if counterGrp == nil {
		t.Fatal("no TypeName group for Counter")
	}

	for _, id := range findIdentsInFunc(ix, "Counter", "expressions.go", "MakeCounterPair") {
		if ix.Group(id) != counterGrp {
			t.Errorf("Counter at %v not linked to Counter type group", ix.Fset.Position(id.Pos()))
		}
	}
}

func TestTypeConversion(t *testing.T) {
	ix := loadTestdata(t)

	counterGrp := findTypeGroup(ix, "Counter", "types.go")
	if counterGrp == nil {
		t.Fatal("no TypeName group for Counter")
	}

	found := false
	for _, id := range findIdentsInFunc(ix, "Counter", "expressions.go", "ToCounter") {
		if ix.Group(id) == counterGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("Counter in type conversion not linked to Counter type group")
	}
}

func TestChannelTypes(t *testing.T) {
	ix := loadTestdata(t)

	eventGrp := findTypeGroup(ix, "Event", "channels.go")
	if eventGrp == nil {
		t.Fatal("no TypeName group for Event")
	}
	for _, id := range findIdentsInFile(ix, "Event", "channels.go") {
		if g := ix.Group(id); g != nil && g != eventGrp && g.Kind == mast.TypeName && g.Name == "Event" {
			t.Errorf("Event ident at %v in different group", ix.Fset.Position(id.Pos()))
		}
	}

	ecGrp := findTypeGroup(ix, "EventChan", "channels.go")
	if ecGrp == nil {
		t.Fatal("no TypeName group for EventChan")
	}
	if ecGrp == eventGrp {
		t.Error("EventChan and Event should be in separate groups")
	}

	erGrp := findTypeGroup(ix, "EventReceiver", "channels.go")
	if erGrp == nil {
		t.Fatal("no TypeName group for EventReceiver")
	}
	if erGrp == eventGrp || erGrp == ecGrp {
		t.Error("EventReceiver should be in its own group")
	}
}

func TestNamedFuncType(t *testing.T) {
	ix := loadTestdata(t)

	grp := findTypeGroup(ix, "Predicate", "expressions.go")
	if grp == nil {
		t.Fatal("no TypeName group for Predicate")
	}
	if len(grp.Idents) < 2 {
		t.Errorf("expected at least 2 Predicate idents, got %d", len(grp.Idents))
	}
}

func TestMapNamedType(t *testing.T) {
	ix := loadTestdata(t)

	grp := findTypeGroup(ix, "UserIndex", "expressions.go")
	if grp == nil {
		t.Fatal("no TypeName group for UserIndex")
	}
	if len(grp.Idents) < 3 {
		t.Errorf("expected at least 3 UserIndex idents, got %d", len(grp.Idents))
	}
}

func TestInterfaceEmbedding(t *testing.T) {
	ix := loadTestdata(t)

	stringerGrp := findTypeGroup(ix, "Stringer", "types.go")
	if stringerGrp == nil {
		t.Fatal("no TypeName group for Stringer in types.go")
	}

	found := false
	for _, id := range findIdentsInFile(ix, "Stringer", "expressions.go") {
		if ix.Group(id) == stringerGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("embedded Stringer in StringerAlt not linked to Stringer type group")
	}
}

func TestAlternateInterfaceSameMethod(t *testing.T) {
	ix := loadTestdata(t)

	// Stringer.String and Alternate.String are separate interface methods.
	var stringerStringGrp, alternateStringGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "String", "types.go") {
		g := ix.Group(id)
		if g == nil || g.Kind != mast.Method {
			continue
		}
		pos := ix.Fset.Position(id.Pos())
		if strings.Contains(pos.Filename, "types.go") {
			if stringerStringGrp == nil {
				stringerStringGrp = g
			} else if g != stringerStringGrp && alternateStringGrp == nil {
				alternateStringGrp = g
			}
		}
	}

	if stringerStringGrp == nil {
		t.Fatal("no Method group for Stringer.String")
	}
	if alternateStringGrp == nil {
		t.Fatal("no Method group for Alternate.String")
	}
	if stringerStringGrp == alternateStringGrp {
		t.Error("Stringer.String() and Alternate.String() should be in separate groups")
	}
}

func TestInterfaceMethodVsConcreteMethod(t *testing.T) {
	ix := loadTestdata(t)

	// Find any String method group from types.go (interface method).
	var stringerStringGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "String", "types.go") {
		g := ix.Group(id)
		if g != nil {
			stringerStringGrp = g
			break
		}
	}
	if stringerStringGrp == nil {
		t.Fatal("no group for String in types.go")
	}

	userStringGrp := findMethodGroup(ix, "String", "funcs.go")
	if userStringGrp == nil {
		t.Fatal("no Method group for User.String in funcs.go")
	}

	if stringerStringGrp == userStringGrp {
		t.Error("Stringer.String() and User.String() should be in separate groups")
	}

	// s.String() in CallStringer should resolve to some Method group.
	for _, id := range findIdentsInFunc(ix, "String", "expressions.go", "CallStringer") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind != mast.Method {
			t.Errorf("s.String() call expected Method kind, got %v", grp.Kind)
		}
	}
}

func TestTypeAssertionAndSwitch(t *testing.T) {
	ix := loadTestdata(t)

	userTypeGrp := findTypeGroup(ix, "User", "structs.go")
	if userTypeGrp == nil {
		t.Fatal("no TypeName group for User in structs.go")
	}

	linked := false
	for _, id := range findIdentsInFunc(ix, "User", "expressions.go", "Describe") {
		if ix.Group(id) == userTypeGrp {
			linked = true
			break
		}
	}
	if !linked {
		t.Error("User in type switch not linked to User type definition")
	}
}

func TestNamedReturnValues(t *testing.T) {
	ix := loadTestdata(t)

	// "result" in Divide and "result" in Filter are separate local vars.
	divideResult := findIdentsInFunc(ix, "result", "expressions.go", "Divide")
	if len(divideResult) == 0 {
		t.Fatal("no result idents in Divide")
	}

	grp := ix.Group(divideResult[0])
	if grp == nil {
		t.Fatal("result named return in Divide has no group")
	}
	if grp.Kind != mast.Var {
		t.Errorf("expected Var kind for result, got %v", grp.Kind)
	}

	for _, id := range divideResult {
		if ix.Group(id) != grp {
			t.Errorf("result ident at %v not in same group within Divide", ix.Fset.Position(id.Pos()))
		}
	}

	// result in Filter should be in a different group.
	filterResult := findIdentsInFunc(ix, "result", "expressions.go", "Filter")
	for _, id := range filterResult {
		if ix.Group(id) == grp {
			t.Errorf("result in Filter should be in separate group from result in Divide")
		}
	}
}

func TestVariadicForwarding(t *testing.T) {
	ix := loadTestdata(t)

	namesGrp := findFuncGroup(ix, "Names", "funcs.go")
	if namesGrp == nil {
		t.Fatal("no Func group for Names in funcs.go")
	}

	found := false
	for _, id := range findIdentsInFunc(ix, "Names", "expressions.go", "FirstName") {
		if ix.Group(id) == namesGrp {
			found = true
			break
		}
	}
	if !found {
		t.Error("Names call in FirstName not linked to Names function group")
	}
}

func TestLabels(t *testing.T) {
	ix := loadTestdata(t)

	outerIdents := findIdentsInFunc(ix, "Outer", "expressions.go", "SearchMatrix")
	if len(outerIdents) == 0 {
		t.Fatal("no Outer idents in SearchMatrix")
	}
	grp := ix.Group(outerIdents[0])
	if grp == nil {
		t.Fatal("Outer label has no group")
	}
	if grp.Kind != mast.Label {
		t.Errorf("expected Label kind for Outer, got %v", grp.Kind)
	}
	for _, id := range outerIdents {
		if ix.Group(id) != grp {
			t.Error("Outer label idents not all in same group")
		}
	}
	if len(grp.Idents) < 2 {
		t.Errorf("expected at least 2 Outer label idents (def + use), got %d", len(grp.Idents))
	}
}

func TestVarConst(t *testing.T) {
	ix := loadTestdata(t)

	for _, name := range []string{"DefaultUser", "MaxUsers", "RoleAdmin"} {
		idents := findIdents(ix, name)
		if len(idents) == 0 {
			t.Errorf("no %s idents found", name)
			continue
		}
		grp := ix.Group(idents[0])
		if grp == nil {
			t.Errorf("%s has no group", name)
			continue
		}
		if grp.Name != name {
			t.Errorf("expected group name %s, got %s", name, grp.Name)
		}
	}
}
