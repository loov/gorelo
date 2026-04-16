package edit

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// checkApply applies p against files under every ordering of p's
// primitives and asserts every permutation produces the same output.
// For each listed path in want, the post-Apply content must match
// exactly; paths not listed in want are not inspected.
//
// Order-independence is a design invariant of the package; this helper
// enforces it. For len(prims) ≤ 8 every permutation (up to 40320) is
// exercised; larger plans fall back to a fixed-seed sample of 2000
// shuffled orderings.
func checkApply(t *testing.T, p *Plan, files map[string][]byte, want map[string]string) {
	t.Helper()
	runAllOrders(t, p, files, func(got map[string][]byte, err error, label string) {
		if err != nil {
			t.Fatalf("%s: Apply error: %v", label, err)
		}
		for path, w := range want {
			if string(got[path]) != w {
				t.Fatalf("%s file %q:\n  got  %q\n  want %q", label, path, got[path], w)
			}
		}
	})
}

// checkApplyConflict asserts that every ordering of p's primitives
// returns a *ConflictError from Apply.
func checkApplyConflict(t *testing.T, p *Plan, files map[string][]byte) {
	t.Helper()
	runAllOrders(t, p, files, func(_ map[string][]byte, err error, label string) {
		var ce *ConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: want *ConflictError, got %v", label, err)
		}
	})
}

// checkApplyError asserts that every ordering of p's primitives returns
// a non-nil error that is not a *ConflictError (e.g., a bounds error).
func checkApplyError(t *testing.T, p *Plan, files map[string][]byte) {
	t.Helper()
	runAllOrders(t, p, files, func(_ map[string][]byte, err error, label string) {
		if err == nil {
			t.Fatalf("%s: want error, got nil", label)
		}
		var ce *ConflictError
		if errors.As(err, &ce) {
			t.Fatalf("%s: want plain error, got *ConflictError: %v", label, err)
		}
	})
}

func runAllOrders(t *testing.T, p *Plan, files map[string][]byte, fn func(got map[string][]byte, err error, label string)) {
	t.Helper()
	prims := p.Primitives()
	if len(prims) <= 8 {
		n := 0
		permute(prims, func(perm []Primitive) {
			n++
			label := fmt.Sprintf("perm %d order %v", n, originsOf(perm))
			pp := &Plan{prims: append([]Primitive(nil), perm...)}
			got, err := pp.Apply(files)
			fn(got, err, label)
		})
		return
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	for k := range 2000 {
		perm := append([]Primitive(nil), prims...)
		rnd.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		label := fmt.Sprintf("sample %d order %v", k+1, originsOf(perm))
		pp := &Plan{prims: perm}
		got, err := pp.Apply(files)
		fn(got, err, label)
	}
}

// permute calls fn for every permutation of items. An empty input
// results in a single call with an empty slice. Items are shuffled in
// place via Heap's algorithm; fn must not retain the slice across
// calls.
func permute(items []Primitive, fn func([]Primitive)) {
	work := make([]Primitive, len(items))
	copy(work, items)
	if len(work) == 0 {
		fn(work)
		return
	}
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

func originsOf(prims []Primitive) []string {
	out := make([]string, len(prims))
	for i, p := range prims {
		out[i] = p.Origin()
	}
	return out
}

// TestPermute verifies the Heap's algorithm implementation: for N items
// it must invoke the callback exactly N! times with every distinct
// ordering; zero items must still invoke once with an empty slice.
func TestPermute(t *testing.T) {
	t.Run("four", func(t *testing.T) {
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
		if count != 24 || len(seen) != 24 {
			t.Errorf("count=%d unique=%d want 24/24", count, len(seen))
		}
	})
	t.Run("empty", func(t *testing.T) {
		count := 0
		permute(nil, func(a []Primitive) {
			count++
			if len(a) != 0 {
				t.Errorf("want empty slice, got %v", a)
			}
		})
		if count != 1 {
			t.Errorf("count=%d want 1", count)
		}
	})
}

func TestApply_EmptyPlan(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	checkApply(t, &p, files, map[string]string{"a.go": "hello"})
}

func TestApply_Insert(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 5}, " world", Before, "greet")
	checkApply(t, &p, files, map[string]string{"a.go": "hello world"})
}

func TestApply_InsertAtStart(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 0}, "> ", Before, "prefix")
	checkApply(t, &p, files, map[string]string{"a.go": "> hello"})
}

func TestApply_InsertAtEndOfFileAnchor(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: -1}, " world", Before, "eof")
	checkApply(t, &p, files, map[string]string{"a.go": "hello world"})
}

func TestApply_Delete(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello, world")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 5, End: 7}, "comma")
	checkApply(t, &p, files, map[string]string{"a.go": "helloworld"})
}

func TestApply_Replace(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hello, world")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 7, End: 12}, "Go", "rename")
	checkApply(t, &p, files, map[string]string{"a.go": "hello, Go"})
}

func TestApply_MultipleDisjointEdits(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("0123456789")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 0, End: 2}, "AA", "a")
	p.Delete(Span{Path: "a.go", Start: 4, End: 6}, "b")
	p.Insert(Anchor{Path: "a.go", Offset: 8}, "X", Before, "c")
	checkApply(t, &p, files, map[string]string{"a.go": "AA2367X89"})
}

func TestApply_TwoInsertsSameAnchorDifferentSide(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AB")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 1}, ">", Before, "before")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "<", After, "after")
	checkApply(t, &p, files, map[string]string{"a.go": "A><B"})
}

func TestApply_InsertAtDeleteStart(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "d")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "Y", Before, "i")
	checkApply(t, &p, files, map[string]string{"a.go": "AYB"})
}

func TestApply_InsertAtDeleteEnd(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "d")
	p.Insert(Anchor{Path: "a.go", Offset: 3}, "Y", After, "i")
	checkApply(t, &p, files, map[string]string{"a.go": "AYB"})
}

func TestApply_MultipleFiles(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("hello"),
		"b.go": []byte("world"),
	}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 0, End: 5}, "HI", "ra")
	p.Replace(Span{Path: "b.go", Start: 0, End: 5}, "THERE", "rb")
	checkApply(t, &p, files, map[string]string{
		"a.go": "HI",
		"b.go": "THERE",
	})
}

func TestApply_IdenticalReplaceDeduped(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 1, End: 3}, "YY", "one")
	p.Replace(Span{Path: "a.go", Start: 1, End: 3}, "YY", "two")
	checkApply(t, &p, files, map[string]string{"a.go": "AYYB"})
}

func TestApply_IdenticalDeleteDeduped(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AxxB")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "one")
	p.Delete(Span{Path: "a.go", Start: 1, End: 3}, "two")
	checkApply(t, &p, files, map[string]string{"a.go": "AB"})
}

func TestApply_IdenticalInsertDeduped(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AB")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "X", Before, "one")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "X", Before, "two")
	checkApply(t, &p, files, map[string]string{"a.go": "AXB"})
}

func TestApply_ConflictOverlappingReplace(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "XX", "a")
	p.Replace(Span{Path: "a.go", Start: 2, End: 5}, "YY", "b")
	checkApplyConflict(t, &p, files)
}

func TestApply_ConflictSameSpanDifferentReplace(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "XX", "a")
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "YY", "b")
	checkApplyConflict(t, &p, files)
}

func TestApply_ConflictDeleteAndReplaceSameSpan(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 4}, "d")
	p.Replace(Span{Path: "a.go", Start: 1, End: 4}, "Y", "r")
	checkApplyConflict(t, &p, files)
}

func TestApply_ConflictInsertInsideDelete(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("abcdef")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 1, End: 4}, "d")
	p.Insert(Anchor{Path: "a.go", Offset: 2}, "X", Before, "i")
	checkApplyConflict(t, &p, files)
}

func TestApply_ConflictTwoInsertsSameAnchorSameSideDifferentText(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("AB")}
	var p Plan
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "X", Before, "one")
	p.Insert(Anchor{Path: "a.go", Offset: 1}, "Y", Before, "two")
	checkApplyConflict(t, &p, files)
}

func TestApply_OutOfBoundsDelete(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("hi")}
	var p Plan
	p.Delete(Span{Path: "a.go", Start: 0, End: 10}, "d")
	checkApplyError(t, &p, files)
}

func TestApply_MoveSameFile(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("Hello, world")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 5}, Anchor{Path: "a.go", Offset: 12}, MoveOptions{}, "m")
	checkApply(t, &p, files, map[string]string{"a.go": ", worldHello"})
}

func TestApply_MoveToOtherFile(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("Hello, world"),
		"b.go": []byte("<>"),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 5}, Anchor{Path: "b.go", Offset: 1}, MoveOptions{}, "m")
	checkApply(t, &p, files, map[string]string{
		"a.go": ", world",
		"b.go": "<Hello>",
	})
}

func TestApply_MoveToNewFile(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("Hello, world")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 5}, Anchor{Path: "new.go", Offset: 0}, MoveOptions{}, "m")
	checkApply(t, &p, files, map[string]string{
		"a.go":   ", world",
		"new.go": "Hello",
	})
}

func TestApply_MoveToEndOfFile(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("ABCDE"),
		"b.go": []byte("xyz"),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 3}, Anchor{Path: "b.go", Offset: -1}, MoveOptions{}, "m")
	checkApply(t, &p, files, map[string]string{
		"a.go": "ADE",
		"b.go": "xyzBC",
	})
}

func TestApply_MoveCarriesReplace(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("prefix[Foo bar]suffix"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 15}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	p.Replace(Span{Path: "a.go", Start: 7, End: 10}, "Baz", "r")
	checkApply(t, &p, files, map[string]string{
		"a.go": "prefixsuffix",
		"b.go": "[Baz bar]",
	})
}

func TestApply_MoveCarriesInsert(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("prefix[x]suffix"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 9}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	p.Insert(Anchor{Path: "a.go", Offset: 7}, "INS", Before, "i")
	checkApply(t, &p, files, map[string]string{
		"a.go": "prefixsuffix",
		"b.go": "[INSx]",
	})
}

func TestApply_MoveCarriesDelete(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("prefix[AB]suffix"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 10}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m")
	p.Delete(Span{Path: "a.go", Start: 7, End: 8}, "d")
	checkApply(t, &p, files, map[string]string{
		"a.go": "prefixsuffix",
		"b.go": "[B]",
	})
}

func TestApply_NestedMoves(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("prefix[ABXYZCD]suffix"),
		"b.go": []byte(""),
		"c.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 6, End: 15}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "outer")
	p.Move(Span{Path: "a.go", Start: 9, End: 12}, Anchor{Path: "c.go", Offset: 0}, MoveOptions{}, "inner")
	checkApply(t, &p, files, map[string]string{
		"a.go": "prefixsuffix",
		"b.go": "[ABCD]",
		"c.go": "XYZ",
	})
}

func TestApply_MoveGroupKeywordMerges(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("<Foo Bar>"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 4}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "const", AppendNewline: true}, "m1")
	p.Move(Span{Path: "a.go", Start: 5, End: 8}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{GroupKeyword: "const", AppendNewline: true}, "m2")
	checkApply(t, &p, files, map[string]string{
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
	checkApplyConflict(t, &p, files)
}

func TestApply_MoveOverlapConflict(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("0123456789")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 5}, Anchor{Path: "a.go", Offset: 10}, MoveOptions{}, "m1")
	p.Move(Span{Path: "a.go", Start: 3, End: 8}, Anchor{Path: "a.go", Offset: 10}, MoveOptions{}, "m2")
	checkApplyConflict(t, &p, files)
}

func TestApply_MoveEqualSpanConflict(t *testing.T) {
	files := map[string][]byte{"a.go": []byte("0123456789")}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 1, End: 5}, Anchor{Path: "a.go", Offset: 10}, MoveOptions{}, "m1")
	p.Move(Span{Path: "a.go", Start: 1, End: 5}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{}, "m2")
	checkApplyConflict(t, &p, files)
}

func TestApply_MoveDedentOption(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("PRE\tfoo\n\tbar\nPOST"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 3, End: 12}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{Dedent: true}, "m")
	checkApply(t, &p, files, map[string]string{"b.go": "foo\nbar"})
}

func TestApply_MoveAppendNewlineOption(t *testing.T) {
	files := map[string][]byte{
		"a.go": []byte("abc"),
		"b.go": []byte(""),
	}
	var p Plan
	p.Move(Span{Path: "a.go", Start: 0, End: 3}, Anchor{Path: "b.go", Offset: 0}, MoveOptions{AppendNewline: true}, "m")
	checkApply(t, &p, files, map[string]string{"b.go": "abc\n"})
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

	checkApply(t, &p, files, map[string]string{
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

	want := "func ServeHTTP(server *Server, w int) {}"
	checkApply(t, &p, files, map[string]string{"a.go": want})
}

func TestApply_UnknownFileTreatedAsEmpty(t *testing.T) {
	files := map[string][]byte{}
	var p Plan
	p.Insert(Anchor{Path: "new.go", Offset: 0}, "hello", Before, "seed")
	checkApply(t, &p, files, map[string]string{"new.go": "hello"})
}

func TestApply_DoesNotMutateInput(t *testing.T) {
	orig := []byte("hello")
	files := map[string][]byte{"a.go": orig}
	var p Plan
	p.Replace(Span{Path: "a.go", Start: 0, End: 5}, "HI", "r")
	checkApply(t, &p, files, map[string]string{"a.go": "HI"})
	if string(orig) != "hello" {
		t.Errorf("input slice mutated: %q", orig)
	}
}
