package mast_test

import (
	"testing"

	"github.com/loov/gorelo/mast"
)

// TestGenericMethodTypeParams verifies that Go 1.27 method-level type
// parameters are tracked as distinct groups per method, and that an
// explicit instantiation of a generic method resolves to the method.
func TestGenericMethodTypeParams(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	applyGM := firstIdentInFunc(t, ix, "GM", "go127.go", "Apply")
	collectGM := firstIdentInFunc(t, ix, "GM", "go127.go", "Collect")
	gApply, gCollect := ix.Group(applyGM), ix.Group(collectGM)
	if gApply == nil || gCollect == nil {
		t.Fatalf("missing groups: apply=%v collect=%v", gApply, gCollect)
	}
	if gApply == gCollect {
		t.Error("Apply's GM and Collect's GM should be distinct groups")
	}

	// l.Apply[GM](f) inside Collect must resolve to the Apply method.
	applyUse := firstIdentInFunc(t, ix, "Apply", "go127.go", "Collect")
	g := ix.Group(applyUse)
	if g == nil || g.Kind != mast.ObjectMethod || g.Name != "Apply" {
		t.Fatalf("explicit instantiation did not resolve to method Apply: %v", g)
	}
	if g.DefIdent() == nil {
		t.Error("Apply group has no definition")
	}
}

// TestPromotedFieldLiteralKey verifies that a Go 1.27 promoted-field
// composite literal key resolves to the embedded struct's field group.
func TestPromotedFieldLiteralKey(t *testing.T) {
	t.Parallel()
	ix := loadTestdata(t)

	key := firstIdentInFunc(t, ix, "PromotedName", "go127.go", "NewPromotedOuter")
	g := ix.Group(key)
	if g == nil || g.Kind != mast.ObjectField {
		t.Fatalf("promoted key not tracked as field: %v", g)
	}
	// The group holds the field definition in PromotedBase plus the
	// literal key — the same group a selector through the embedded
	// field would join.
	if g.DefIdent() == nil || len(g.Idents) != 2 {
		t.Errorf("promoted key should share PromotedBase.PromotedName's group, got %d idents", len(g.Idents))
	}
}
