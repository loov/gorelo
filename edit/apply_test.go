package edit

import (
	"errors"
	"math/rand/v2"
	"strings"
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

// checkApplyAllOrders asserts that applying prims yields want regardless of
// the order in which they were added to a Plan. Order-independence is a
// design invariant of the package; this helper enforces it.
//
// For len(prims) ≤ 8 every permutation (up to 40320) is exercised; larger
// inputs fall back to a fixed-seed sample of 2000 shuffled orderings to
// keep test runtime bounded.
func checkApplyAllOrders(t *testing.T, prims []Primitive, files map[string][]byte, want map[string]string) {
	t.Helper()
	if len(prims) <= 8 {
		n := 0
		permute(prims, func(perm []Primitive) {
			n++
			runOnePerm(t, n, perm, files, want)
		})
		return
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	for k := range 2000 {
		perm := append([]Primitive(nil), prims...)
		rnd.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		runOnePerm(t, k+1, perm, files, want)
	}
}

func runOnePerm(t *testing.T, n int, perm []Primitive, files map[string][]byte, want map[string]string) {
	t.Helper()
	p := &Plan{prims: append([]Primitive(nil), perm...)}
	got, err := p.Apply(files)
	if err != nil {
		t.Fatalf("perm %d order %v: Apply error: %v", n, originsOf(perm), err)
	}
	for path, w := range want {
		if string(got[path]) != w {
			t.Fatalf("perm %d order %v file %q:\n  got  %q\n  want %q",
				n, originsOf(perm), path, got[path], w)
		}
	}
}

// permute calls fn for every permutation of items. Items are shuffled in
// place via Heap's algorithm; fn must not retain the slice across calls.
func permute(items []Primitive, fn func([]Primitive)) {
	work := make([]Primitive, len(items))
	copy(work, items)
	permuteHelp(work, len(work), fn)
}

func permuteHelp(a []Primitive, k int, fn func([]Primitive)) {
	if k == 1 {
		fn(a)
		return
	}
	for i := range k {
		permuteHelp(a, k-1, fn)
		if k%2 == 0 {
			a[i], a[k-1] = a[k-1], a[i]
		} else {
			a[0], a[k-1] = a[k-1], a[0]
		}
	}
}

// TestPermute verifies the Heap's algorithm implementation: for N items it
// must invoke the callback exactly N! times with every distinct ordering.
func TestPermute(t *testing.T) {
	items := []Primitive{
		Insert{origin: "a"},
		Insert{origin: "b"},
		Insert{origin: "c"},
		Insert{origin: "d"},
	}
	count := 0
	seen := map[string]bool{}
	permute(items, func(a []Primitive) {
		count++
		seen[strings.Join(originsOf(a), ",")] = true
	})
	if want := 24; count != want {
		t.Errorf("total calls: got %d, want %d", count, want)
	}
	if len(seen) != 24 {
		t.Errorf("unique orderings: got %d, want 24", len(seen))
	}
}

func originsOf(prims []Primitive) []string {
	out := make([]string, len(prims))
	for i, p := range prims {
		out[i] = p.Origin()
	}
	return out
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

func TestApply_MoveSameFile(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("Hello, world")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 5}, Anchor{Path: "a.go", Offset: 12}, MoveOptions{}, "m")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", ", worldHello")
}

func TestApply_MoveToOtherFile(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("Hello, world"),
		"b.go": []byte("<>"),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 5}, Anchor{Path: "b.go", Offset: 1}, MoveOptions{}, "m")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", ", world")
	assertFile(t, out, "b.go", "<Hello>")
}

func TestApply_MoveToNewFile(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("Hello, world")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 5}, Anchor{Path: "new.go", Offset: 0}, MoveOptions{}, "m")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", ", world")
	assertFile(t, out, "new.go", "Hello")
}

func TestApply_MoveToEndOfFile(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("ABCDE"),
		"b.go": []byte("xyz"),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 3}, Anchor{Path: "b.go", Offset: -1}, MoveOptions{}, "m")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "ADE")
	assertFile(t, out, "b.go", "xyzBC")
}

func TestApply_MoveCarriesReplace(t *testing.T) {
	// Input: "prefix[Foo bar]suffix" — move "[Foo bar]" (positions 6..15)
	// to another file while renaming Foo→Baz inside the moved region.
	files := map[string][]byte{
		"a.go": []byte("prefix[Foo bar]suffix"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 15}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	p.Replace(Span{Path: "a.go", Start: 7, End: 10}, "Baz", "r") // "Foo" → "Baz" inside the moved span
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "prefixsuffix")
	assertFile(t, out, "b.go", "[Baz bar]")
}

func TestApply_MoveCarriesInsert(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("prefix[x]suffix"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 9}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	p.Insert(Anchor{Path: "a.go", Offset: 7}, "INS", Before, "i") // inside moved span, before 'x'
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "prefixsuffix")
	assertFile(t, out, "b.go", "[INSx]")
}

func TestApply_MoveCarriesDelete(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("prefix[AB]suffix"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 10}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	p.Delete(Span{Path: "a.go", Start: 7, End: 8}, "d") // delete 'A' inside moved span
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "prefixsuffix")
	assertFile(t, out, "b.go", "[B]")
}

func TestApply_NestedMoves(t *testing.T) {
	// Outer: move [6, 15) to b.go. Inner: move [8, 11) from that range to c.go.
	// Outer's realized content excludes [8,11); inner's content emits at c.go.
	files := map[string][]byte{
		"a.go": []byte("prefix[ABXYZCD]suffix"),
		"b.go": []byte(""),
		"c.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 15}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "outer")
	p.Move(Span{Path: "a.go", Start: 9, End: 12}, Anchor{Path: "c.go", Offset: 0}, MoveOptions{}, "inner")
	out := mustApply(t, &p, files)
	assertFile(t, out, "a.go", "prefixsuffix")
	assertFile(t, out, "b.go", "[ABCD]")
	assertFile(t, out, "c.go", "XYZ")
}

func TestApply_MoveGroupKeywordMerges(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("<Foo Bar>"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 4}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "const", AppendNewline: true}, "m1")
	p.Move(Span{Path: "a.go", Start: 5, End: 8}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "const", AppendNewline: true}, "m2")
	checkApplyAllOrders(t, p.Primitives(), files, map[string]string{
		"a.go": "< >",
		"b.go": "const (\nFoo\nBar\n)\n",
	})
}

func TestApply_MoveGroupKeywordMismatchConflict(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("<Foo Bar>"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 4}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "const"}, "m1")
	p.Move(Span{Path: "a.go", Start: 5, End: 8}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "var"}, "m2")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_MoveOverlapConflict(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("0123456789")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 5}, Anchor{Path: "a.go", Offset: 10}, MoveOptions{}, "m1")
	p.Move(Span{Path: "a.go", Start: 3, End: 8}, Anchor{Path: "a.go", Offset: 10}, MoveOptions{}, "m2")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_MoveEqualSpanConflict(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("0123456789")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 5}, Anchor{Path: "a.go", Offset: 10}, MoveOptions{}, "m1")
	p.Move(Span{Path: "a.go", Start: 1, End: 5}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m2")
	_, err := p.Apply(files)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestApply_MoveDedentOption(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("PRE\tfoo\n\tbar\nPOST"),
		"b.go": []byte(""),
	}
	// Move span: "\tfoo\n\tbar" at [3, 12). Dedent strips the shared tab.
	var p Plan
	p.Move(Span{Path: "a.go", Start: 3, End: 12}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{Dedent: true}, "m")
	out := mustApply(t, &p, files)
	assertFile(t, out, "b.go", "foo\nbar")
}

func TestApply_MoveAppendNewlineOption(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("abc"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 3}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{AppendNewline: true}, "m")
	out := mustApply(t, &p, files)
	assertFile(t, out, "b.go", "abc\n")
}

func TestApply_StressMoveRenameDetach(t *testing.T) {
	// Simultaneous composition of four operations on the same method:
	//   1. Move the type decl to a new file
	//   2. Rename the type (Server → Host) inside the moved type decl
	//   3. Rename the method (Handle → Serve)
	//   4. Detach the method (receiver → first parameter, qualified as *Host)
	//   5. Move the method to the same new file
	//
	// Offsets are computed explicitly; comments show the span each primitive
	// targets in the input.
	input := "package src\n" + // [0, 12)
		"type Server struct{ x int }\n" + // [12, 40)
		"func (s *Server) Handle(n int) error { return nil }\n" // [40, 92)

	files := map[string][]byte{
		"a.go": []byte(input),
		"b.go": []byte(""),
	}

	const (
		typeDeclStart = 12
		typeDeclEnd   = 40
		typeNameStart = 17 // "Server" inside the type decl
		typeNameEnd   = 23

		funcDeclStart = 40
		funcDeclEnd   = 92
		recvStart     = 45 // "(s *Server) " — paren through trailing space
		recvEnd       = 57
		methodStart   = 57 // "Handle"
		methodEnd     = 63
		paramInsertAt = 64 // position of 'n' in "(n int)"
	)

	var p Plan

	// Outer Move #1: type decl → b.go:0
	p.Move(Span{Path: "a.go", Start: typeDeclStart, End: typeDeclEnd},
		Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "move-type")
	// Carried: Server → Host inside the type decl.
	p.Replace(Span{Path: "a.go", Start: typeNameStart, End: typeNameEnd},
		"Host", "rename-type")

	// Outer Move #2: func decl → b.go:0 (merges with Move #1 at same anchor
	// with matching empty GroupKeyword; realized bytes concatenate in plan
	// order).
	p.Move(Span{Path: "a.go", Start: funcDeclStart, End: funcDeclEnd},
		Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "move-func")
	// Carried: delete "(s *Server) " — the whole receiver + paren + trailing space.
	p.Delete(Span{Path: "a.go", Start: recvStart, End: recvEnd}, "detach-strip-recv")
	// Carried: rename Handle → Serve.
	p.Replace(Span{Path: "a.go", Start: methodStart, End: methodEnd},
		"Serve", "rename-method")
	// Carried: insert the renamed receiver as first parameter.
	p.Insert(Anchor{Path: "a.go", Offset: paramInsertAt},
		"s *Host, ", Before, "detach-add-recv-param")

	wantA := "package src\n"
	wantB := "type Host struct{ x int }\n" +
		"func Serve(s *Host, n int) error { return nil }\n"

	checkApplyAllOrders(t, p.Primitives(), files, map[string]string{
		"a.go": wantA,
		"b.go": wantB,
	})
}

func TestApply_DetachMethodExample(t *testing.T) {
	// The user's driving example:
	//   func (server *Server) ServeHTTP(w int) {}
	// ↓ four primitives ↓
	//   func ServeHTTP(server *Server, w int) {}
	src := "func (server *Server) ServeHTTP(w int) {}"
	files := map[string][]byte{"a.go": []byte(src)}

	var p Plan
	// 1. Move "server *Server" (bytes [6, 20)) to just after '(' of ServeHTTP (offset 32).
	p.Move(Span{Path: "a.go", Start: 6, End: 20}, Anchor{Path: "a.go", Offset: 32}, MoveOptions{}, "detach-recv")
	// 2. Insert ", " right after the Move's destination (Side=After so it follows the Move's Before-Insert).
	p.Insert(Anchor{Path: "a.go", Offset: 32}, ", ", After, "detach-sep")
	// 3. Delete " (" preceding the receiver (bytes [4, 6)).
	p.Delete(Span{Path: "a.go", Start: 4, End: 6}, "detach-openparen")
	// 4. Delete ")" after the receiver (byte [20, 21)).
	p.Delete(Span{Path: "a.go", Start: 20, End: 21}, "detach-closeparen")

	out := mustApply(t, &p, files)
	want := "func ServeHTTP(server *Server, w int) {}"
	assertFile(t, out, "a.go", want)
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
