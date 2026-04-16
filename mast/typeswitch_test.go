package mast_test

import (
	"testing"
)

// TestTypeSwitchGuardGroupContainsCaseBody verifies that the guard
// ident of a type switch and its uses inside case bodies resolve to
// the same group. go/types records the guard binding in
// info.Implicits (one entry per case clause) rather than info.Defs;
// resolveInfo's type-switch pass registers the guard ident as a Def
// with the first case's implicit object, and objectKeyFor's pos-based
// Scope key makes every case's implicit collapse onto the same key.
func TestTypeSwitchGuardGroupContainsCaseBody(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	ids := findIdentsInFunc(ix, "tvGuard", "scope_extras.go", "TypeSwitchGuard")
	if len(ids) < 2 {
		t.Fatalf("expected at least 2 tvGuard idents (guard + case uses), got %d", len(ids))
	}
	var groups []any
	for _, id := range ids {
		grp := ix.Group(id)
		if grp == nil {
			t.Errorf("tvGuard ident has no group")
			continue
		}
		groups = append(groups, grp)
	}
	if len(groups) < 2 {
		t.Fatal("not enough grouped idents to compare")
	}
	first := groups[0]
	for i, g := range groups[1:] {
		if g != first {
			t.Errorf("tvGuard ident %d resolved to a different group than ident 0", i+1)
		}
	}
}
