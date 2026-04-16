package edit

import "testing"

func TestPlan_CollectsPrimitives(t *testing.T) {
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 10}, "hello", Before, "src-a")
	p.Delete(Span{Path: "a.go", Start: 20, End: 30}, "src-b")
	p.Replace(Span{Path: "a.go", Start: 40, End: 50}, "world", "src-c")
	p.Move(Span{Path: "a.go", Start: 60, End: 70}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "const"}, "src-d")

	prims := p.Primitives()
	if len(prims) != 4 {
		t.Fatalf("want 4 primitives, got %d", len(prims))
	}

	ins, ok := prims[0].(Insert)
	if !ok {
		t.Fatalf("prims[0]: want Insert, got %T", prims[0])
	}
	if ins.Anchor.Path != "a.go" || ins.Anchor.Offset != 10 || ins.Text != "hello" || ins.Side != Before {
		t.Errorf("Insert: got %+v", ins)
	}
	if ins.Origin() != "src-a" {
		t.Errorf("Insert.Origin: want src-a, got %q", ins.Origin())
	}

	del, ok := prims[1].(Delete)
	if !ok {
		t.Fatalf("prims[1]: want Delete, got %T", prims[1])
	}
	if del.Span != (Span{Path: "a.go", Start: 20, End: 30}) || del.Origin() != "src-b" {
		t.Errorf("Delete: got %+v, origin %q", del, del.Origin())
	}

	rep, ok := prims[2].(Replace)
	if !ok {
		t.Fatalf("prims[2]: want Replace, got %T", prims[2])
	}
	if rep.Span != (Span{Path: "a.go", Start: 40, End: 50}) || rep.Text != "world" || rep.Origin() != "src-c" {
		t.Errorf("Replace: got %+v, origin %q", rep, rep.Origin())
	}

	mov, ok := prims[3].(Move)
	if !ok {
		t.Fatalf("prims[3]: want Move, got %T", prims[3])
	}
	if mov.Span != (Span{Path: "a.go", Start: 60, End: 70}) {
		t.Errorf("Move.Span: got %+v", mov.Span)
	}
	if mov.Dest != (Anchor{Path: "b.go", Offset: 0}) {
		t.Errorf("Move.Dest: got %+v", mov.Dest)
	}
	if mov.Options.GroupKeyword != "const" {
		t.Errorf("Move.Options.GroupKeyword: got %q", mov.Options.GroupKeyword)
	}
	if mov.Origin() != "src-d" {
		t.Errorf("Move.Origin: got %q", mov.Origin())
	}
}

func TestPlan_PrimitivesIsCopy(t *testing.T) {
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 0}, "x", Before, "o")
	prims := p.Primitives()
	prims[0] = Delete{}
	if _, ok := p.Primitives()[0].(Insert); !ok {
		t.Error("mutating returned slice leaked into Plan")
	}
}

func TestConflictError_Message(t *testing.T) {
	err := &ConflictError{
		A:      Insert{origin: "rename"},
		B:      Delete{origin: "self-import"},
		Reason: "overlap at boundary",
	}
	want := `edit conflict between "rename" and "self-import": overlap at boundary`
	if got := err.Error(); got != want {
		t.Errorf("Error():\n  got  %q\n  want %q", got, want)
	}
}
