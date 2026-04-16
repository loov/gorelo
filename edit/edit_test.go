package edit

import (
	"strings"
	"testing"
)

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

func TestConflictError_MessageWithoutFrames(t *testing.T) {
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

func TestPlan_NonDebugOmitsFrames(t *testing.T) {
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 0}, "x", Before, "o")
	if fr := p.Primitives()[0].Frames(); fr != nil {
		t.Errorf("non-debug plan recorded frames: %v", fr)
	}
}

func TestPlan_DebugRecordsFrames(t *testing.T) {
	p := Plan{Debug: true}
	p.Insert(Anchor{Path: "a.go", Offset: 0}, "x", Before, "o")
	p.Delete(Span{Path: "a.go", Start: 0, End: 1}, "o")
	p.Replace(Span{Path: "a.go", Start: 0, End: 1}, "y", "o")
	p.Move(Span{Path: "a.go", Start: 0, End: 1}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "o")

	for i, prim := range p.Primitives() {
		frames := prim.Frames()
		if len(frames) == 0 {
			t.Errorf("primitive %d (%T): no frames captured", i, prim)
			continue
		}
		top := frames[0]
		if !strings.HasSuffix(top.File, "/edit/edit_test.go") {
			t.Errorf("primitive %d (%T): top frame %s not in this test file", i, prim, top.File)
		}
		if !strings.Contains(top.Function, "TestPlan_DebugRecordsFrames") {
			t.Errorf("primitive %d (%T): top frame function %q not the test", i, prim, top.Function)
		}
	}
}

func TestConflictError_MessageIncludesFrames(t *testing.T) {
	p := Plan{Debug: true}
	p.Replace(Span{Path: "a.go", Start: 0, End: 3}, "XX", "one")
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "YY", "two")
	_, err := p.Apply(map[string][]byte{"a.go": []byte("abcdef")})
	if err == nil {
		t.Fatal("want conflict")
	}
	msg := err.Error()
	if !strings.Contains(msg, "A added at") || !strings.Contains(msg, "B added at") {
		t.Errorf("error message missing frame references:\n%s", msg)
	}
	if !strings.Contains(msg, "edit_test.go") {
		t.Errorf("error message missing test file reference:\n%s", msg)
	}
}
