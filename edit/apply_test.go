package edit

import (
	"errors"
	"testing"
)

func mustApply(t *testing.T, p *Plan, files map[string][]byte) map[string][]byte {
	t.Helper()
	out, err := p.Apply(files)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out
}

func assertFile(t *testing.T, out map[string][]byte, path, want string) {
	t.Helper()
	got, ok := out[path]
	if !ok {
		t.Fatalf("file %q missing from output", path)
	}
	if string(got) != want {
		t.Errorf("file %q:\n  got  %q\n  want %q", path, got, want)
	}
}

func TestApply_EmptyPlan(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "hello")
}

func TestApply_Insert(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 5}, " world", Before, "greet")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "hello world")
}

func TestApply_InsertAtStart(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 0}, "> ", Before, "prefix")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "> hello")
}

func TestApply_InsertAtEndOfFileAnchor(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: -1}, " world", Before, "eof")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "hello world")
}

func TestApply_Delete(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello, world")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 5, End: 7}, "comma")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "helloworld")
}

func TestApply_Replace(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello, world")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 7, End: 12}, "Go", "rename")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "hello, Go")
}

func TestApply_MultipleDisjointEdits(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("0123456789")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 0, End: 2}, "AA", "a")
	p.Delete(Span{Path: "a.go", Start: 4, End: 6}, "b")
	p.Insert(Anchor{Path: "a.go", Offset: 8}, "X", Before, "c")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "AA2367X89")
}

func TestApply_TwoInsertsSameAnchorDifferentSide(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AB")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 1}, ">", Before, "before")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "<", After, "after")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "A><B")
}

func TestApply_InsertAtDeleteStart(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "d")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "Y", Before, "i")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "AYB")
}

func TestApply_InsertAtDeleteEnd(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "d")
	p.Insert(Anchor{Path: "a.go", Offset: 3}, "Y", After, "i")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "AYB")
}

func TestApply_MultipleFiles(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("hello"),
		"b.go": []byte("world"),
	}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 0, End: 5}, "HI", "ra")
	p.Replace(Span{Path: "b.go", Start: 0, End: 5}, "THERE", "rb")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "HI")
	assertFile(t, out, "b.go", "THERE")
}

func TestApply_IdenticalReplaceDeduped(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 1, End: 3}, "YY", "one")
	p.Replace(Span{Path: "a.go", Start: 1, End: 3}, "YY", "two")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "AYYB")
}

func TestApply_IdenticalDeleteDeduped(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "one")
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "two")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "AB")
}

func TestApply_IdenticalInsertDeduped(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AB")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "X", Before, "one")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "X", Before, "two")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "AXB")
}

func TestApply_ConflictOverlappingReplace(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "XX", "a")
	p.Replace(Span{Path: "a.go", Start: 2, End: 5}, "YY", "b")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_ConflictSameSpanDifferentReplace(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "XX", "a")
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "YY", "b")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_ConflictDeleteAndReplaceSameSpan(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 4}, "d")
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "Y", "r")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_ConflictInsertInsideDelete(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 4}, "d")
	p.Insert(Anchor{Path: "a.go", Offset: 2}, "X", Before, "i")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_ConflictTwoInsertsSameAnchorSameSideDifferentText(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AB")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "X", Before, "one")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "Y", Before, "two")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_OutOfBoundsDelete(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hi")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 0, End: 10}, "d")
	_, err := p.Apply(files)
	if err == nil {
		t.Fatal("want bounds error")
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		t.Fatalf("want plain error, got ConflictError: %v", err)
	}
}

func TestApply_MoveNotYetSupported(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hi")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 2}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	_, err := p.Apply(files)
	if err == nil {
		t.Fatal("want error for unsupported Move")
	}
}

func TestApply_UnknownFileTreatedAsEmpty(t *testing.T) {
	files := map[string][]byte{}
	var p Plan
	p.Insert(Anchor{Path: "new.go", Offset: 0}, "hello", Before, "seed")
	out := mustApply(t, &p, files)
	assertFile(t, out, "new.go", "hello")
}

func TestApply_DoesNotMutateInput(t *testing.T) {
	orig := []byte("hello")
	files := map[string][]byte{"a.go": orig}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 0, End: 5}, "HI", "r")
	mustApply(t, &p, files)
	if string(orig) != "hello" {
		t.Errorf("input slice mutated: %q", orig)
	}
}
