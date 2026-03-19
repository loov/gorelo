package mast_test

import (
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestFindDef(t *testing.T) {
	ix := loadTestdata(t)

	tests := []struct {
		name   string
		source string
		want   string // expected Group.Kind, or "" for nil result
	}{
		// Types
		{name: "User", want: "TypeName"},
		{name: "Counter", want: "TypeName"},
		{name: "Role", want: "TypeName"},
		{name: "Pair", want: "TypeName"},
		{name: "Stringer", want: "TypeName"},
		{name: "Node", want: "TypeName"},

		// Functions
		{name: "NewUser", want: "Func"},
		{name: "MakePair", want: "Func"},
		{name: "Names", want: "Func"},

		// Variables
		{name: "DefaultUser", want: "Var"},
		{name: "ErrNotFound", want: "Var"},

		// Constants
		{name: "MaxUsers", want: "Const"},
		{name: "RoleGuest", want: "Const"},

		// Not found
		{name: "DoesNotExist", want: ""},
		{name: "doesnotexist", want: ""},
	}

	kindString := map[mast.ObjectKind]string{
		mast.TypeName: "TypeName",
		mast.Func:     "Func",
		mast.Var:      "Var",
		mast.Const:    "Const",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := ix.FindDef(tt.name, tt.source)
			if tt.want == "" {
				if id != nil {
					t.Errorf("FindDef(%q, %q) = %s, want nil", tt.name, tt.source, id.Name)
				}
				return
			}
			if id == nil {
				t.Fatalf("FindDef(%q, %q) = nil, want %s", tt.name, tt.source, tt.want)
			}
			grp := ix.Group(id)
			if grp == nil {
				t.Fatalf("FindDef(%q, %q) returned ident with no group", tt.name, tt.source)
			}
			got := kindString[grp.Kind]
			if got != tt.want {
				t.Errorf("FindDef(%q, %q) kind = %s, want %s", tt.name, tt.source, got, tt.want)
			}
			if id.Name != tt.name {
				t.Errorf("FindDef(%q, %q) ident name = %q", tt.name, tt.source, id.Name)
			}
		})
	}
}

func TestFindDefSourcePackage(t *testing.T) {
	ix := loadTestdata(t)

	// "Info" exists in example/linux but not in the root example package.
	id := ix.FindDef("Info", "example/linux")
	if id == nil {
		t.Fatal("FindDef(\"Info\", \"example/linux\") = nil")
	}
	if id.Name != "Info" {
		t.Errorf("ident name = %q, want \"Info\"", id.Name)
	}

	// Searching in the wrong package returns nil.
	id = ix.FindDef("Info", "example")
	if id != nil {
		t.Errorf("FindDef(\"Info\", \"example\") = %s, want nil", id.Name)
	}
}

func TestFindDefSourceFile(t *testing.T) {
	ix := loadTestdata(t)

	// "User" is defined in structs.go.
	id := ix.FindDef("User", "structs.go")
	if id == nil {
		t.Fatal("FindDef(\"User\", \"structs.go\") = nil")
	}

	// "User" is not defined in types.go.
	id = ix.FindDef("User", "types.go")
	if id != nil {
		t.Errorf("FindDef(\"User\", \"types.go\") = %s, want nil", id.Name)
	}
}

func TestFindDefSubpackageFunc(t *testing.T) {
	ix := loadTestdata(t)

	// "Name" exists in example/linux.
	id := ix.FindDef("Name", "example/linux")
	if id == nil {
		t.Fatal("FindDef(\"Name\", \"example/linux\") = nil")
	}

	grp := ix.Group(id)
	if grp == nil || grp.Kind != mast.Func {
		t.Errorf("expected Func kind for Name in example/linux")
	}
}

func TestFindDefReturnsDefIdent(t *testing.T) {
	ix := loadTestdata(t)

	// The returned ident must be the definition, not a use.
	id := ix.FindDef("Server", "")
	if id == nil {
		t.Fatal("FindDef(\"Server\", \"\") = nil")
	}

	grp := ix.Group(id)
	if grp == nil {
		t.Fatal("Server has no group")
	}

	// Verify the returned ident is the Def in the group.
	var found bool
	for _, gid := range grp.Idents {
		if gid.Ident == id && gid.Kind == mast.Def {
			found = true
			break
		}
	}
	if !found {
		t.Error("FindDef returned an ident that is not marked as Def in its group")
	}
}

func TestFindFieldDef(t *testing.T) {
	ix := loadTestdata(t)

	tests := []struct {
		desc      string
		typeName  string
		fieldName string
		source    string
		wantNil   bool
	}{
		{desc: "User.Name", typeName: "User", fieldName: "Name"},
		{desc: "User.Email", typeName: "User", fieldName: "Email"},
		{desc: "User.Age", typeName: "User", fieldName: "Age"},
		{desc: "Server.Addr", typeName: "Server", fieldName: "Addr"},
		{desc: "Node.Value", typeName: "Node", fieldName: "Value"},
		{desc: "Node.Children", typeName: "Node", fieldName: "Children"},
		{desc: "Pair.First", typeName: "Pair", fieldName: "First"},
		{desc: "Pair.Second", typeName: "Pair", fieldName: "Second"},
		{desc: "Admin.Permissions", typeName: "Admin", fieldName: "Permissions"},

		// Anonymous struct in var declaration.
		{desc: "Config.Host", typeName: "Config", fieldName: "Host"},
		{desc: "Config.Port", typeName: "Config", fieldName: "Port"},

		// Not found cases.
		{desc: "User.Missing", typeName: "User", fieldName: "Missing", wantNil: true},
		{desc: "Missing.Name", typeName: "Missing", fieldName: "Name", wantNil: true},
		{desc: "Counter.X (not a struct)", typeName: "Counter", fieldName: "X", wantNil: true},
		{desc: "Config.Missing", typeName: "Config", fieldName: "Missing", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			id := ix.FindFieldDef(tt.typeName, tt.fieldName, tt.source)
			if tt.wantNil {
				if id != nil {
					t.Errorf("FindFieldDef(%q, %q, %q) = %s, want nil", tt.typeName, tt.fieldName, tt.source, id.Name)
				}
				return
			}
			if id == nil {
				t.Fatalf("FindFieldDef(%q, %q, %q) = nil", tt.typeName, tt.fieldName, tt.source)
			}
			if id.Name != tt.fieldName {
				t.Errorf("ident name = %q, want %q", id.Name, tt.fieldName)
			}
			grp := ix.Group(id)
			if grp == nil {
				t.Fatalf("returned ident has no group")
			}
			if grp.Kind != mast.Field {
				t.Errorf("group kind = %d, want Field", grp.Kind)
			}
		})
	}
}

func TestFindFieldDefSourceFilter(t *testing.T) {
	ix := loadTestdata(t)

	// User.Name is in structs.go.
	id := ix.FindFieldDef("User", "Name", "structs.go")
	if id == nil {
		t.Fatal("FindFieldDef(\"User\", \"Name\", \"structs.go\") = nil")
	}

	// User.Name is not in types.go.
	id = ix.FindFieldDef("User", "Name", "types.go")
	if id != nil {
		t.Error("FindFieldDef(\"User\", \"Name\", \"types.go\") should be nil")
	}

	// Info.Distro is in example/linux.
	id = ix.FindFieldDef("Info", "Distro", "example/linux")
	if id == nil {
		t.Fatal("FindFieldDef(\"Info\", \"Distro\", \"example/linux\") = nil")
	}

	// Info.Distro is not in example root.
	id = ix.FindFieldDef("Info", "Distro", "example")
	if id != nil {
		t.Error("FindFieldDef(\"Info\", \"Distro\", \"example\") should be nil")
	}
}

func TestFindFieldDefReturnsDefIdent(t *testing.T) {
	ix := loadTestdata(t)

	id := ix.FindFieldDef("User", "Email", "")
	if id == nil {
		t.Fatal("FindFieldDef(\"User\", \"Email\", \"\") = nil")
	}

	grp := ix.Group(id)
	if grp == nil {
		t.Fatal("Email has no group")
	}

	var found bool
	for _, gid := range grp.Idents {
		if gid.Ident == id && gid.Kind == mast.Def {
			found = true
			break
		}
	}
	if !found {
		t.Error("FindFieldDef returned an ident that is not marked as Def in its group")
	}
}
