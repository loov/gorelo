package mast_test

import (
	"go/ast"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/loov/mast"
)

func loadTestdata(t *testing.T) *mast.Index {
	t.Helper()
	dir, err := filepath.Abs("testdata/example")
	if err != nil {
		t.Fatal(err)
	}
	ix, err := mast.Load(&mast.Config{Dir: dir}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// findIdents returns all *ast.Ident with the given name across all packages/files.
func findIdents(ix *mast.Index, name string) []*ast.Ident {
	var result []*ast.Ident
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == name {
					result = append(result, id)
				}
				return true
			})
		}
	}
	return result
}

// findIdentsInFile returns all *ast.Ident with name in files whose path contains pathFragment.
func findIdentsInFile(ix *mast.Index, name, pathFragment string) []*ast.Ident {
	var result []*ast.Ident
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			if !strings.Contains(file.Path, pathFragment) {
				continue
			}
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == name {
					result = append(result, id)
				}
				return true
			})
		}
	}
	return result
}

func TestLoad(t *testing.T) {
	ix := loadTestdata(t)

	if len(ix.Pkgs) == 0 {
		t.Fatal("expected at least one package")
	}

	// We should have the main "example" package.
	var found bool
	for _, pkg := range ix.Pkgs {
		if pkg.Path == "example" {
			found = true
			// Should have files from all platforms.
			hasLinux := false
			hasWindows := false
			for _, f := range pkg.Files {
				if strings.Contains(f.Path, "platform_linux.go") {
					hasLinux = true
				}
				if strings.Contains(f.Path, "platform_windows.go") {
					hasWindows = true
				}
			}
			if !hasLinux {
				t.Error("missing platform_linux.go")
			}
			if !hasWindows {
				t.Error("missing platform_windows.go")
			}
		}
	}
	if !found {
		t.Error("example package not found")
	}
}

func TestTypeRename(t *testing.T) {
	ix := loadTestdata(t)

	// "Counter" appears in types.go (def), types.go (alias target), vars.go (const type).
	idents := findIdents(ix, "Counter")
	if len(idents) == 0 {
		t.Fatal("no Counter idents found")
	}

	grp := ix.Group(idents[0])
	if grp == nil {
		t.Fatal("Counter ident has no group")
	}

	// All Counter idents should be in the same group.
	for _, id := range idents {
		g := ix.Group(id)
		if g != grp {
			t.Errorf("Counter ident %v at pos %v in different group", id.Name, id.Pos())
		}
	}

	if grp.Kind != mast.TypeName {
		t.Errorf("expected TypeName kind, got %v", grp.Kind)
	}
}

func TestFieldRename(t *testing.T) {
	ix := loadTestdata(t)

	// "Name" field defined in structs.go, used in funcs.go (u.Name, Name: name).
	idents := findIdents(ix, "Name")
	if len(idents) == 0 {
		t.Fatal("no Name idents found")
	}

	// Find the field definition (in User struct).
	var fieldGroup *mast.Group
	for _, id := range idents {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Field {
			fieldGroup = grp
			break
		}
	}
	if fieldGroup == nil {
		t.Fatal("no Field group found for Name")
	}

	// Should have multiple idents (def + uses).
	if len(fieldGroup.Idents) < 2 {
		t.Errorf("expected at least 2 idents in Name field group, got %d", len(fieldGroup.Idents))
	}
}

func TestSameNameFieldsDifferentStructs(t *testing.T) {
	ix := loadTestdata(t)

	// User.Name and File.Name are fields with the same name in different structs.
	// They must be in separate groups. This was previously broken when multi-pass
	// type-checking (for platform-specific files) merged them via identical keys.

	// Find the User.Name group (defined in structs.go).
	var userNameGroup *mast.Group
	for _, id := range findIdentsInFile(ix, "Name", "structs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Field {
			userNameGroup = grp
			break
		}
	}
	if userNameGroup == nil {
		t.Fatal("no Field group for User.Name in structs.go")
	}

	// Find the File.Name group (defined in platform_linux.go or platform_windows.go).
	var fileNameGroup *mast.Group
	for _, pathFrag := range []string{"platform_linux.go", "platform_windows.go"} {
		for _, id := range findIdentsInFile(ix, "Name", pathFrag) {
			grp := ix.Group(id)
			if grp != nil && grp.Kind == mast.Field {
				fileNameGroup = grp
				break
			}
		}
		if fileNameGroup != nil {
			break
		}
	}
	if fileNameGroup == nil {
		t.Fatal("no Field group for File.Name in platform files")
	}

	if userNameGroup == fileNameGroup {
		t.Error("User.Name and File.Name must be in separate groups, but were merged into one")
	}

	// Verify f.Name in platform_common.go (PrintName method) is in File.Name's group, not User.Name's.
	for _, id := range findIdentsInFile(ix, "Name", "platform_common.go") {
		grp := ix.Group(id)
		if grp == userNameGroup {
			pos := ix.Fset.Position(id.Pos())
			t.Errorf("Name at %s is in User.Name group but should be in File.Name group", pos)
		}
	}
}

func TestEmbeddedField(t *testing.T) {
	ix := loadTestdata(t)

	// The embedded "User" in Admin struct should be linked to the User type def.
	// Find idents named "User".
	idents := findIdents(ix, "User")
	if len(idents) == 0 {
		t.Fatal("no User idents found")
	}

	// All User idents referring to the type should be in the same group.
	var typeGroup *mast.Group
	for _, id := range idents {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.TypeName {
			typeGroup = grp
			break
		}
	}
	if typeGroup == nil {
		t.Fatal("no TypeName group for User")
	}

	// The embedded field ident in Admin should be in this group.
	if len(typeGroup.Idents) < 2 {
		t.Errorf("expected User type group to have at least 2 idents (def + embedded), got %d", len(typeGroup.Idents))
	}

	// Check that at least one ident is from structs.go (the embedded field).
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

func TestMethodRename(t *testing.T) {
	ix := loadTestdata(t)

	// "String" method defined on User.
	idents := findIdents(ix, "String")
	if len(idents) == 0 {
		t.Fatal("no String idents found")
	}

	var methodGroup *mast.Group
	for _, id := range idents {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Method {
			methodGroup = grp
			break
		}
	}
	if methodGroup == nil {
		t.Fatal("no Method group for String")
	}

	if methodGroup.Name != "String" {
		t.Errorf("expected group name String, got %s", methodGroup.Name)
	}
}

func TestCrossFilePlatformName(t *testing.T) {
	ix := loadTestdata(t)

	// PlatformName is defined in platform_linux.go and platform_windows.go,
	// called in platform_common.go. All should be in the same group.
	idents := findIdents(ix, "PlatformName")
	if len(idents) == 0 {
		t.Fatal("no PlatformName idents found")
	}

	grp := ix.Group(idents[0])
	if grp == nil {
		t.Fatal("PlatformName has no group")
	}

	for _, id := range idents {
		g := ix.Group(id)
		if g != grp {
			t.Error("PlatformName idents not all in same group")
			break
		}
	}

	// Should have at least 3 idents: 2 defs (linux, windows) + 1 use (common).
	if len(grp.Idents) < 3 {
		t.Errorf("expected at least 3 PlatformName idents, got %d", len(grp.Idents))
	}
}

func TestCrossPackage(t *testing.T) {
	ix := loadTestdata(t)

	// Find "Name" idents in the linux or windows subpackage files.
	// The linux.Name() call in platform_linux.go should link to Name def in linux/linux.go.
	var subpkgName string
	var platformFile string
	if runtime.GOOS == "linux" {
		subpkgName = "linux"
		platformFile = "platform_linux.go"
	} else {
		// On non-linux/windows, the deps loaded by go/packages will be for host platform.
		// We'll check whichever subpackage was loaded.
		subpkgName = "linux" // default check
		platformFile = "platform_linux.go"
	}

	// Find Name idents in the subpackage.
	subIdents := findIdentsInFile(ix, "Name", subpkgName+"/"+subpkgName+".go")
	if len(subIdents) == 0 {
		t.Skipf("no %s subpackage Name idents found (may not be loaded on %s)", subpkgName, runtime.GOOS)
	}

	subGrp := ix.Group(subIdents[0])
	if subGrp == nil {
		t.Fatalf("%s.Name has no group", subpkgName)
	}

	// Find the selector "Name" in the platform file (linux.Name()).
	platIdents := findIdentsInFile(ix, "Name", platformFile)
	if len(platIdents) == 0 {
		t.Skipf("no Name idents found in %s", platformFile)
	}

	// At least one of the platform file's Name idents should be in the same group.
	linked := false
	for _, id := range platIdents {
		if ix.Group(id) == subGrp {
			linked = true
			break
		}
	}
	if !linked {
		t.Errorf("cross-package link: %s.Name() call not linked to %s.Name definition", subpkgName, subpkgName)
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

func TestGenericTypeParams(t *testing.T) {
	ix := loadTestdata(t)

	// Pair[A, B] — A and B type params should each have their own group.
	// Find A idents in types.go.
	aIdents := findIdentsInFile(ix, "A", "types.go")
	if len(aIdents) == 0 {
		t.Fatal("no A idents in types.go")
	}

	grp := ix.Group(aIdents[0])
	if grp == nil {
		t.Fatal("type param A has no group")
	}

	// All A idents in types.go should be in the same group.
	for _, id := range aIdents {
		if ix.Group(id) != grp {
			t.Error("type param A idents not all in same group")
			break
		}
	}
}

func TestAnonymousStructFields(t *testing.T) {
	ix := loadTestdata(t)

	// Config.Host — the Host field in the anonymous struct var.
	hostIdents := findIdentsInFile(ix, "Host", "structs.go")
	if len(hostIdents) == 0 {
		t.Fatal("no Host idents in structs.go")
	}

	grp := ix.Group(hostIdents[0])
	if grp == nil {
		t.Fatal("Host field has no group")
	}

	if grp.Kind != mast.Field {
		t.Errorf("expected Field kind for Host, got %v", grp.Kind)
	}

	// Should have at least 2 idents (field def + composite literal key).
	if len(grp.Idents) < 2 {
		t.Errorf("expected at least 2 Host field idents, got %d", len(grp.Idents))
	}
}

func TestAllPackagesLoaded(t *testing.T) {
	ix := loadTestdata(t)

	paths := map[string]bool{}
	for _, pkg := range ix.Pkgs {
		paths[pkg.Path] = true
	}

	// We should have example, example/linux, and example/windows.
	for _, want := range []string{"example", "example/linux", "example/windows"} {
		if !paths[want] {
			t.Errorf("package %s not loaded", want)
		}
	}
}

func TestPlatformSpecificType(t *testing.T) {
	ix := loadTestdata(t)

	// "File" is defined in both platform_linux.go and platform_windows.go
	// with overlapping fields. All File type idents should be in the same group.
	fileIdents := findIdents(ix, "File")
	if len(fileIdents) == 0 {
		t.Fatal("no File idents found")
	}

	var typeGroup *mast.Group
	for _, id := range fileIdents {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.TypeName {
			typeGroup = grp
			break
		}
	}
	if typeGroup == nil {
		t.Fatal("no TypeName group for File")
	}

	// File type defs from both platform files should be in the same group.
	hasLinux := false
	hasWindows := false
	for _, id := range typeGroup.Idents {
		if strings.Contains(id.File.Path, "platform_linux.go") {
			hasLinux = true
		}
		if strings.Contains(id.File.Path, "platform_windows.go") {
			hasWindows = true
		}
	}
	if !hasLinux {
		t.Error("File type def from platform_linux.go not in group")
	}
	if !hasWindows {
		t.Error("File type def from platform_windows.go not in group")
	}
}

func TestPlatformSpecificTypeMethod(t *testing.T) {
	ix := loadTestdata(t)

	// PrintName method is defined in platform_common.go on *File.
	// It should be grouped as a method.
	printIdents := findIdentsInFile(ix, "PrintName", "platform_common.go")
	if len(printIdents) == 0 {
		t.Fatal("no PrintName idents in platform_common.go")
	}

	grp := ix.Group(printIdents[0])
	if grp == nil {
		t.Fatal("PrintName has no group")
	}
	if grp.Kind != mast.Method {
		t.Errorf("expected Method kind for PrintName, got %v", grp.Kind)
	}
}

func TestPlatformSpecificCommonField(t *testing.T) {
	ix := loadTestdata(t)

	// "Name" field exists in File on both platforms and is used in
	// platform_common.go via f.Name. The field access in PrintName
	// should be in the same group as the field defs.
	//
	// Find Name idents that are Fields in platform files.
	var fieldGroup *mast.Group
	for _, pathFrag := range []string{"platform_linux.go", "platform_windows.go"} {
		nameIdents := findIdentsInFile(ix, "Name", pathFrag)
		for _, id := range nameIdents {
			grp := ix.Group(id)
			if grp != nil && grp.Kind == mast.Field && grp.Name == "Name" {
				fieldGroup = grp
				break
			}
		}
		if fieldGroup != nil {
			break
		}
	}
	if fieldGroup == nil {
		t.Fatal("no Field group for File.Name")
	}

	// The f.Name use in platform_common.go should be in this group.
	commonNameIdents := findIdentsInFile(ix, "Name", "platform_common.go")
	linked := false
	for _, id := range commonNameIdents {
		if ix.Group(id) == fieldGroup {
			linked = true
			break
		}
	}
	if !linked {
		t.Error("f.Name in platform_common.go not linked to File.Name field group")
	}
}

func TestPlatformSpecificUniqueFields(t *testing.T) {
	ix := loadTestdata(t)

	// FD is linux-only, Handle is windows-only. Each should have its own group.
	fdIdents := findIdentsInFile(ix, "FD", "platform_linux.go")
	if len(fdIdents) == 0 {
		t.Fatal("no FD idents in platform_linux.go")
	}
	fdGrp := ix.Group(fdIdents[0])
	if fdGrp == nil {
		t.Fatal("FD has no group")
	}
	if fdGrp.Kind != mast.Field {
		t.Errorf("expected Field kind for FD, got %v", fdGrp.Kind)
	}

	handleIdents := findIdentsInFile(ix, "Handle", "platform_windows.go")
	if len(handleIdents) == 0 {
		t.Fatal("no Handle idents in platform_windows.go")
	}
	handleGrp := ix.Group(handleIdents[0])
	if handleGrp == nil {
		t.Fatal("Handle has no group")
	}
	if handleGrp.Kind != mast.Field {
		t.Errorf("expected Field kind for Handle, got %v", handleGrp.Kind)
	}

	// They should be in different groups.
	if fdGrp == handleGrp {
		t.Error("FD and Handle should be in different groups")
	}
}

func TestFieldThroughTypeAlias(t *testing.T) {
	ix := loadTestdata(t)

	// Member is a type alias for User. Accessing m.Name through a Member
	// should resolve to the same User.Name field group.

	// Find the User.Name field group from structs.go.
	var userNameGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "Name", "structs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Field {
			userNameGrp = grp
			break
		}
	}
	if userNameGrp == nil {
		t.Fatal("no Field group for User.Name in structs.go")
	}

	// The m.Name in MemberName (accessing Name through the Member alias)
	// should be in the same group as User.Name.
	// MemberName is in structs.go; find the Name ident that is a Use.
	found := false
	for _, ident := range userNameGrp.Idents {
		pos := ix.Fset.Position(ident.Ident.Pos())
		if strings.Contains(pos.Filename, "structs.go") && ident.Kind == mast.Use {
			// This should be the m.Name use inside MemberName.
			found = true
			break
		}
	}
	if !found {
		t.Error("m.Name in MemberName (via type alias Member) not linked to User.Name field group")
	}
}

func TestChannelTypes(t *testing.T) {
	ix := loadTestdata(t)

	// Event type should link across channels.go usages.
	eventIdents := findIdentsInFile(ix, "Event", "channels.go")
	if len(eventIdents) == 0 {
		t.Fatal("no Event idents in channels.go")
	}
	grp := ix.Group(eventIdents[0])
	if grp == nil {
		t.Fatal("Event has no group")
	}
	if grp.Kind != mast.TypeName {
		t.Errorf("expected TypeName kind for Event, got %v", grp.Kind)
	}
	for _, id := range eventIdents {
		if g := ix.Group(id); g != grp {
			t.Errorf("Event ident at %v in different group", ix.Fset.Position(id.Pos()))
		}
	}

	// EventChan should be its own type group, separate from Event.
	ecIdents := findIdentsInFile(ix, "EventChan", "channels.go")
	if len(ecIdents) == 0 {
		t.Fatal("no EventChan idents in channels.go")
	}
	ecGrp := ix.Group(ecIdents[0])
	if ecGrp == nil {
		t.Fatal("EventChan has no group")
	}
	if ecGrp == grp {
		t.Error("EventChan and Event should be in separate groups")
	}

	// EventReceiver should be its own group too.
	erIdents := findIdentsInFile(ix, "EventReceiver", "channels.go")
	if len(erIdents) == 0 {
		t.Fatal("no EventReceiver idents in channels.go")
	}
	erGrp := ix.Group(erIdents[0])
	if erGrp == nil {
		t.Fatal("EventReceiver has no group")
	}
	if erGrp == grp || erGrp == ecGrp {
		t.Error("EventReceiver should be in its own group")
	}
}

func TestLabels(t *testing.T) {
	ix := loadTestdata(t)

	// "Outer" label in SearchMatrix — def and use should be in same group.
	outerIdents := findIdentsInFile(ix, "Outer", "advanced.go")
	if len(outerIdents) == 0 {
		t.Fatal("no Outer idents in advanced.go")
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

func TestNamedFuncType(t *testing.T) {
	ix := loadTestdata(t)

	// Predicate is a named function type.
	predIdents := findIdentsInFile(ix, "Predicate", "advanced.go")
	if len(predIdents) == 0 {
		t.Fatal("no Predicate idents in advanced.go")
	}
	grp := ix.Group(predIdents[0])
	if grp == nil {
		t.Fatal("Predicate has no group")
	}
	if grp.Kind != mast.TypeName {
		t.Errorf("expected TypeName kind for Predicate, got %v", grp.Kind)
	}
	// Should have def + use in Filter parameter.
	if len(grp.Idents) < 2 {
		t.Errorf("expected at least 2 Predicate idents, got %d", len(grp.Idents))
	}
}

func TestNamedReturnValues(t *testing.T) {
	ix := loadTestdata(t)

	// "result" named return in Divide — def and uses within Divide should be grouped.
	// Note: "result" also appears in Filter as a different local var.
	resultIdents := findIdentsInFile(ix, "result", "advanced.go")
	if len(resultIdents) == 0 {
		t.Fatal("no result idents in advanced.go")
	}

	// Find the group for the "result" in Divide (around line 43-49).
	var divideResultGrp *mast.Group
	for _, id := range resultIdents {
		pos := ix.Fset.Position(id.Pos())
		if pos.Line >= 43 && pos.Line <= 49 {
			divideResultGrp = ix.Group(id)
			break
		}
	}
	if divideResultGrp == nil {
		t.Fatal("result named return in Divide has no group")
	}
	if divideResultGrp.Kind != mast.Var {
		t.Errorf("expected Var kind for result, got %v", divideResultGrp.Kind)
	}

	// All "result" idents within Divide should be in the same group.
	for _, id := range resultIdents {
		pos := ix.Fset.Position(id.Pos())
		if pos.Line >= 43 && pos.Line <= 49 {
			if ix.Group(id) != divideResultGrp {
				t.Errorf("result ident at %v not in same group as Divide's named return", pos)
			}
		}
	}

	// "result" in Filter (around line 26) should be in a DIFFERENT group.
	for _, id := range resultIdents {
		pos := ix.Fset.Position(id.Pos())
		if pos.Line >= 25 && pos.Line <= 33 {
			if ix.Group(id) == divideResultGrp {
				t.Errorf("result in Filter at %v should be in separate group from result in Divide", pos)
			}
		}
	}
}

func TestMapNamedType(t *testing.T) {
	ix := loadTestdata(t)

	// UserIndex is a named map type.
	uiIdents := findIdents(ix, "UserIndex")
	if len(uiIdents) == 0 {
		t.Fatal("no UserIndex idents found")
	}
	grp := ix.Group(uiIdents[0])
	if grp == nil {
		t.Fatal("UserIndex has no group")
	}
	if grp.Kind != mast.TypeName {
		t.Errorf("expected TypeName kind for UserIndex, got %v", grp.Kind)
	}
	// def + uses in BuildIndex and LookupUser.
	if len(grp.Idents) < 3 {
		t.Errorf("expected at least 3 UserIndex idents, got %d", len(grp.Idents))
	}
}

func TestTypeAssertionAndSwitch(t *testing.T) {
	ix := loadTestdata(t)

	// In Describe, type switch uses User, Counter, Event — each should link
	// to their respective type groups.
	userIdents := findIdentsInFile(ix, "User", "advanced.go")
	if len(userIdents) == 0 {
		t.Fatal("no User idents in advanced.go")
	}
	// The User ident in the type switch should be in the same group as User struct def.
	userDefIdents := findIdentsInFile(ix, "User", "structs.go")
	if len(userDefIdents) == 0 {
		t.Fatal("no User idents in structs.go")
	}
	var userTypeGrp *mast.Group
	for _, id := range userDefIdents {
		g := ix.Group(id)
		if g != nil && g.Kind == mast.TypeName {
			userTypeGrp = g
			break
		}
	}
	if userTypeGrp == nil {
		t.Fatal("no TypeName group for User in structs.go")
	}

	linked := false
	for _, id := range userIdents {
		if ix.Group(id) == userTypeGrp {
			linked = true
			break
		}
	}
	if !linked {
		t.Error("User in advanced.go type switch not linked to User type definition")
	}
}

func TestCompoundBuildTags(t *testing.T) {
	ix := loadTestdata(t)

	// platform_linux_amd64.go (linux && amd64) and platform_linux_arm64.go (linux && arm64)
	// both define Arch and ArchBits with conflicting definitions.
	// They should be loaded and partitioned into separate type-check passes.

	// Both files should be present.
	hasAmd64 := false
	hasArm64 := false
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "platform_linux_amd64.go") {
				hasAmd64 = true
				if f.BuildTag != "linux && amd64" {
					t.Errorf("expected build tag 'linux && amd64', got %q", f.BuildTag)
				}
			}
			if strings.Contains(f.Path, "platform_linux_arm64.go") {
				hasArm64 = true
				if f.BuildTag != "linux && arm64" {
					t.Errorf("expected build tag 'linux && arm64', got %q", f.BuildTag)
				}
			}
		}
	}
	if !hasAmd64 {
		t.Error("platform_linux_amd64.go not loaded")
	}
	if !hasArm64 {
		t.Error("platform_linux_arm64.go not loaded")
	}

	// Arch var is defined in both files — they should be in the same group
	// (same package-level var name, merged across passes).
	archIdents := findIdents(ix, "Arch")
	if len(archIdents) == 0 {
		t.Fatal("no Arch idents found")
	}
	grp := ix.Group(archIdents[0])
	if grp == nil {
		t.Fatal("Arch has no group")
	}
	for _, id := range archIdents {
		if ix.Group(id) != grp {
			t.Error("Arch idents not all in same group across compound tags")
		}
	}

	// ArchBits is defined in both files — should also merge.
	abIdents := findIdents(ix, "ArchBits")
	if len(abIdents) == 0 {
		t.Fatal("no ArchBits idents found")
	}
	abGrp := ix.Group(abIdents[0])
	if abGrp == nil {
		t.Fatal("ArchBits has no group")
	}
	if abGrp.Kind != mast.Func {
		t.Errorf("expected Func kind for ArchBits, got %v", abGrp.Kind)
	}
}

func TestOrBuildTag(t *testing.T) {
	ix := loadTestdata(t)

	// platform_unix.go has //go:build linux || darwin
	var found bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "platform_unix.go") {
				found = true
				if f.BuildTag != "linux || darwin" {
					t.Errorf("expected build tag 'linux || darwin', got %q", f.BuildTag)
				}
			}
		}
	}
	if !found {
		t.Error("platform_unix.go not loaded")
	}

	// IsUnix should have a group.
	idents := findIdentsInFile(ix, "IsUnix", "platform_unix.go")
	if len(idents) == 0 {
		t.Fatal("no IsUnix idents found")
	}
	grp := ix.Group(idents[0])
	if grp == nil {
		t.Fatal("IsUnix has no group")
	}
	if grp.Kind != mast.Func {
		t.Errorf("expected Func kind for IsUnix, got %v", grp.Kind)
	}
}

func TestNegatedBuildTag(t *testing.T) {
	ix := loadTestdata(t)

	// platform_not_windows.go has //go:build !windows
	var found bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "platform_not_windows.go") {
				found = true
				if f.BuildTag != "!windows" {
					t.Errorf("expected build tag '!windows', got %q", f.BuildTag)
				}
			}
		}
	}
	if !found {
		t.Error("platform_not_windows.go not loaded")
	}

	idents := findIdentsInFile(ix, "IsWindows", "platform_not_windows.go")
	if len(idents) == 0 {
		t.Fatal("no IsWindows idents found")
	}
	grp := ix.Group(idents[0])
	if grp == nil {
		t.Fatal("IsWindows has no group")
	}
	if grp.Kind != mast.Func {
		t.Errorf("expected Func kind for IsWindows, got %v", grp.Kind)
	}
}

func TestCustomBuildTag(t *testing.T) {
	ix := loadTestdata(t)

	// custom_tag.go has //go:build custom (non-standard tag)
	var found bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "custom_tag.go") {
				found = true
				if f.BuildTag != "custom" {
					t.Errorf("expected build tag 'custom', got %q", f.BuildTag)
				}
			}
		}
	}
	if !found {
		t.Error("custom_tag.go not loaded")
	}

	// CustomFeature and CustomGreeting should have groups.
	for _, name := range []string{"CustomFeature", "CustomGreeting"} {
		idents := findIdentsInFile(ix, name, "custom_tag.go")
		if len(idents) == 0 {
			t.Errorf("no %s idents found", name)
			continue
		}
		grp := ix.Group(idents[0])
		if grp == nil {
			t.Errorf("%s has no group", name)
		}
	}
}

func TestLocalVariableScoping(t *testing.T) {
	ix := loadTestdata(t)

	// "name" is a parameter in LookupUser and also in ValidateUser.
	// These are different local variables and must be in separate groups.
	lookupNames := findIdentsInFile(ix, "name", "advanced.go")
	if len(lookupNames) == 0 {
		t.Fatal("no name idents in advanced.go")
	}

	// Collect distinct groups for "name" idents in advanced.go.
	nameGroups := map[*mast.Group]bool{}
	for _, id := range lookupNames {
		grp := ix.Group(id)
		if grp != nil {
			nameGroups[grp] = true
		}
	}
	if len(nameGroups) < 2 {
		t.Errorf("expected 'name' parameter in LookupUser and ValidateUser to be in separate groups, got %d group(s)", len(nameGroups))
	}
}

func TestLocalOkVariableScoping(t *testing.T) {
	ix := loadTestdata(t)

	// "ok" appears as a local in LookupUser and ValidateUser.
	// They must be in separate groups.
	okIdents := findIdentsInFile(ix, "ok", "advanced.go")
	if len(okIdents) == 0 {
		t.Fatal("no ok idents in advanced.go")
	}

	okGroups := map[*mast.Group]bool{}
	for _, id := range okIdents {
		grp := ix.Group(id)
		if grp != nil {
			okGroups[grp] = true
		}
	}
	if len(okGroups) < 2 {
		t.Errorf("expected 'ok' in LookupUser and ValidateUser to be in separate groups, got %d group(s)", len(okGroups))
	}
}

func TestLocalShadowingPackageVar(t *testing.T) {
	ix := loadTestdata(t)

	// ShadowDefaultUser declares a local "DefaultUser" that shadows the
	// package-level var. They must be in separate groups.
	pkgIdents := findIdentsInFile(ix, "DefaultUser", "vars.go")
	if len(pkgIdents) == 0 {
		t.Fatal("no DefaultUser idents in vars.go")
	}
	pkgGrp := ix.Group(pkgIdents[0])
	if pkgGrp == nil {
		t.Fatal("package-level DefaultUser has no group")
	}

	localIdents := findIdentsInFile(ix, "DefaultUser", "advanced.go")
	// There are two kinds: the use of the package-level DefaultUser (in init())
	// and the local shadow in ShadowDefaultUser. The local shadow should be in
	// a different group.
	localGroups := map[*mast.Group]bool{}
	for _, id := range localIdents {
		grp := ix.Group(id)
		if grp != nil {
			localGroups[grp] = true
		}
	}
	if len(localGroups) < 2 {
		t.Errorf("expected local shadow of DefaultUser to be in separate group from package var, got %d group(s)", len(localGroups))
	}
}

func TestPromotedFieldMultiLevel(t *testing.T) {
	ix := loadTestdata(t)

	// SuperAdmin embeds Admin which embeds User.
	// sa.Name should resolve to User.Name field group.
	var userNameGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "Name", "structs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Field {
			// Find the one with User as owner by checking if it has a def in structs.go
			for _, ident := range grp.Idents {
				if ident.Kind == mast.Def && strings.Contains(ident.File.Path, "structs.go") {
					pos := ix.Fset.Position(ident.Ident.Pos())
					// User.Name is on an early line, File.Name would be in platform files.
					if pos.Line < 15 {
						userNameGrp = grp
						break
					}
				}
			}
		}
		if userNameGrp != nil {
			break
		}
	}
	if userNameGrp == nil {
		t.Fatal("no User.Name field group found")
	}

	// The sa.Name in SuperAdminName should be in this group.
	found := false
	for _, ident := range userNameGrp.Idents {
		pos := ix.Fset.Position(ident.Ident.Pos())
		if strings.Contains(pos.Filename, "structs.go") && ident.Kind == mast.Use {
			// Check it's from SuperAdminName (not MemberName or other uses).
			if pos.Line > 55 && pos.Line < 70 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("sa.Name in SuperAdminName not linked to User.Name field group (multi-level promoted field)")
	}
}

func TestSameMethodNameDifferentTypes(t *testing.T) {
	ix := loadTestdata(t)

	// User.String() and Server.String() should be in separate groups.
	var userStringGrp, serverStringGrp *mast.Group

	for _, id := range findIdentsInFile(ix, "String", "funcs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Method {
			userStringGrp = grp
			break
		}
	}
	for _, id := range findIdentsInFile(ix, "String", "structs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Method {
			serverStringGrp = grp
			break
		}
	}

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

func TestSelectorOnReturnValue(t *testing.T) {
	ix := loadTestdata(t)

	// NewUser("test", "test@test.com").Name — the .Name selector should
	// resolve to User.Name field group.
	var userNameGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "Name", "structs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Field {
			for _, ident := range grp.Idents {
				if ident.Kind == mast.Def && strings.Contains(ident.File.Path, "structs.go") {
					pos := ix.Fset.Position(ident.Ident.Pos())
					if pos.Line < 15 {
						userNameGrp = grp
						break
					}
				}
			}
		}
		if userNameGrp != nil {
			break
		}
	}
	if userNameGrp == nil {
		t.Fatal("no User.Name field group found")
	}

	// DefaultUserName calls NewUser(...).Name — find that Name use.
	found := false
	for _, ident := range userNameGrp.Idents {
		pos := ix.Fset.Position(ident.Ident.Pos())
		if strings.Contains(pos.Filename, "structs.go") && ident.Kind == mast.Use {
			// DefaultUserName is around line 62-64
			if pos.Line > 60 && pos.Line < 70 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("NewUser(...).Name selector not linked to User.Name field group")
	}
}

func TestBlankIdentifierUntracked(t *testing.T) {
	ix := loadTestdata(t)

	// Blank identifiers (_) should be untracked (Group returns nil).
	// Find _ idents in advanced.go (IgnoreError uses _ = Divide).
	blankIdents := findIdentsInFile(ix, "_", "advanced.go")
	for _, id := range blankIdents {
		if grp := ix.Group(id); grp != nil {
			t.Errorf("blank identifier at %v should be untracked but has group %q", ix.Fset.Position(id.Pos()), grp.Name)
		}
	}
}

func TestPackageNameUntracked(t *testing.T) {
	ix := loadTestdata(t)

	// The "example" ident in "package example" declarations should be
	// untracked (obj is nil for package name defs).
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

func TestInterfaceMethodVsConcreteMethod(t *testing.T) {
	ix := loadTestdata(t)

	// Stringer.String() (interface method def in types.go) and
	// User.String() (concrete method def in funcs.go) should be in
	// separate groups. Calling s.String() on a Stringer value should
	// resolve to the interface method, not the concrete one.

	// Find Stringer.String group from types.go.
	var stringerStringGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "String", "types.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Method {
			stringerStringGrp = grp
			break
		}
	}
	if stringerStringGrp == nil {
		// It might be classified as Func rather than Method for interface methods.
		for _, id := range findIdentsInFile(ix, "String", "types.go") {
			grp := ix.Group(id)
			if grp != nil {
				stringerStringGrp = grp
				break
			}
		}
	}
	if stringerStringGrp == nil {
		t.Fatal("no group for String in types.go")
	}

	// Find User.String group from funcs.go.
	var userStringGrp *mast.Group
	for _, id := range findIdentsInFile(ix, "String", "funcs.go") {
		grp := ix.Group(id)
		if grp != nil && grp.Kind == mast.Method {
			userStringGrp = grp
			break
		}
	}
	if userStringGrp == nil {
		t.Fatal("no Method group for User.String in funcs.go")
	}

	if stringerStringGrp == userStringGrp {
		t.Error("Stringer.String() and User.String() should be in separate groups")
	}

	// s.String() call in CallStringer should resolve to Stringer.String, not User.String.
	callIdents := findIdentsInFile(ix, "String", "advanced.go")
	for _, id := range callIdents {
		grp := ix.Group(id)
		if grp == userStringGrp {
			pos := ix.Fset.Position(id.Pos())
			// Only flag if it's the one inside CallStringer
			if pos.Line > 100 {
				t.Errorf("s.String() in CallStringer at %v resolved to User.String group instead of Stringer.String", pos)
			}
		}
	}
}

func TestGroupDeduplication(t *testing.T) {
	ix := loadTestdata(t)

	// Verify that no group has duplicate ident pointers.
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file.Syntax, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				grp := ix.Group(id)
				if grp == nil {
					return true
				}
				count := 0
				for _, gid := range grp.Idents {
					if gid.Ident == id {
						count++
					}
				}
				if count > 1 {
					t.Errorf("ident %s at %v appears %d times in its group", id.Name, id.Pos(), count)
				}
				return true
			})
		}
	}
}
