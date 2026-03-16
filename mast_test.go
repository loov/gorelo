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
