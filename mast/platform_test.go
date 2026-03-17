package mast_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestPlatformSpecificType(t *testing.T) {
	ix := loadTestdata(t)

	fileIdents := findIdents(ix, "File")
	if len(fileIdents) == 0 {
		t.Fatal("no File idents found")
	}

	typeGroup := findTypeGroup(ix, "File", "platform_linux.go")
	if typeGroup == nil {
		typeGroup = findTypeGroup(ix, "File", "platform_windows.go")
	}
	if typeGroup == nil {
		t.Fatal("no TypeName group for File")
	}

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

	grp := findMethodGroup(ix, "PrintName", "platform_common.go")
	if grp == nil {
		t.Fatal("PrintName has no group")
	}
	if grp.Kind != mast.Method {
		t.Errorf("expected Method kind for PrintName, got %v", grp.Kind)
	}
}

func TestPlatformSpecificCommonField(t *testing.T) {
	ix := loadTestdata(t)

	var fieldGroup *mast.Group
	for _, pathFrag := range []string{"platform_linux.go", "platform_windows.go"} {
		fieldGroup = findFieldGroup(ix, "Name", pathFrag)
		if fieldGroup != nil {
			break
		}
	}
	if fieldGroup == nil {
		t.Fatal("no Field group for File.Name")
	}

	// f.Name in platform_common.go should be in this group.
	found := false
	for _, id := range findIdentsInFile(ix, "Name", "platform_common.go") {
		if ix.Group(id) == fieldGroup {
			found = true
			break
		}
	}
	if !found {
		t.Error("f.Name in platform_common.go not linked to File.Name field group")
	}
}

func TestPlatformSpecificUniqueFields(t *testing.T) {
	ix := loadTestdata(t)

	fdGrp := findFieldGroup(ix, "FD", "platform_linux.go")
	if fdGrp == nil {
		t.Fatal("FD has no group")
	}

	handleGrp := findFieldGroup(ix, "Handle", "platform_windows.go")
	if handleGrp == nil {
		t.Fatal("Handle has no group")
	}

	if fdGrp == handleGrp {
		t.Error("FD and Handle should be in different groups")
	}
}

func TestCrossFilePlatformName(t *testing.T) {
	ix := loadTestdata(t)

	idents := findIdents(ix, "PlatformName")
	if len(idents) == 0 {
		t.Fatal("no PlatformName idents found")
	}

	grp := ix.Group(idents[0])
	if grp == nil {
		t.Fatal("PlatformName has no group")
	}
	for _, id := range idents {
		if ix.Group(id) != grp {
			t.Error("PlatformName idents not all in same group")
			break
		}
	}
	if len(grp.Idents) < 3 {
		t.Errorf("expected at least 3 PlatformName idents, got %d", len(grp.Idents))
	}
}

func TestCrossPackage(t *testing.T) {
	ix := loadTestdata(t)

	var subpkgName, platformFile string
	if runtime.GOOS == "linux" {
		subpkgName = "linux"
		platformFile = "platform_linux.go"
	} else {
		subpkgName = "linux"
		platformFile = "platform_linux.go"
	}

	subIdents := findIdentsInFile(ix, "Name", subpkgName+"/"+subpkgName+".go")
	if len(subIdents) == 0 {
		t.Skipf("no %s subpackage Name idents found", subpkgName)
	}

	subGrp := ix.Group(subIdents[0])
	if subGrp == nil {
		t.Fatalf("%s.Name has no group", subpkgName)
	}

	platIdents := findIdentsInFile(ix, "Name", platformFile)
	if len(platIdents) == 0 {
		t.Skipf("no Name idents found in %s", platformFile)
	}

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

func TestCompoundBuildTags(t *testing.T) {
	ix := loadTestdata(t)

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

	abGrp := findFuncGroup(ix, "ArchBits", "platform_linux_amd64.go")
	if abGrp == nil {
		abGrp = findFuncGroup(ix, "ArchBits", "platform_linux_arm64.go")
	}
	if abGrp == nil {
		t.Fatal("ArchBits has no group")
	}
}

func TestOrBuildTag(t *testing.T) {
	ix := loadTestdata(t)

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

	grp := findFuncGroup(ix, "IsUnix", "platform_unix.go")
	if grp == nil {
		t.Fatal("IsUnix has no group")
	}
}

func TestNegatedBuildTag(t *testing.T) {
	ix := loadTestdata(t)

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

	grp := findFuncGroup(ix, "IsWindows", "platform_not_windows.go")
	if grp == nil {
		t.Fatal("IsWindows has no group")
	}
}

func TestCustomBuildTag(t *testing.T) {
	ix := loadTestdata(t)

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

	for _, name := range []string{"CustomFeature", "CustomGreeting"} {
		idents := findIdentsInFile(ix, name, "custom_tag.go")
		if len(idents) == 0 {
			t.Errorf("no %s idents found", name)
			continue
		}
		if ix.Group(idents[0]) == nil {
			t.Errorf("%s has no group", name)
		}
	}
}

func TestBuildIgnoreFile(t *testing.T) {
	ix := loadTestdata(t)

	var found bool
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if strings.Contains(f.Path, "ignored.go") {
				found = true
				if f.BuildTag != "ignore" {
					t.Errorf("expected build tag 'ignore', got %q", f.BuildTag)
				}
			}
		}
	}
	if !found {
		t.Error("ignored.go not loaded")
	}

	for _, name := range []string{"IgnoredVar", "IgnoredFunc"} {
		idents := findIdentsInFile(ix, name, "ignored.go")
		if len(idents) == 0 {
			t.Errorf("no %s idents found in ignored.go", name)
			continue
		}
		if ix.Group(idents[0]) == nil {
			t.Errorf("%s has no group", name)
		}
	}
}
