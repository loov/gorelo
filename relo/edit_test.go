package relo

import (
	"testing"
)

func TestDeduplicateEdits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []edit
		want  int // expected count
	}{
		{
			name:  "nil",
			input: nil,
			want:  0,
		},
		{
			name:  "empty",
			input: []edit{},
			want:  0,
		},
		{
			name: "no duplicates",
			input: []edit{
				{Start: 0, End: 3, New: "a"},
				{Start: 5, End: 8, New: "b"},
			},
			want: 2,
		},
		{
			name: "identical duplicates",
			input: []edit{
				{Start: 0, End: 3, New: "a"},
				{Start: 0, End: 3, New: "a"},
				{Start: 10, End: 15, New: "c"},
			},
			want: 2,
		},
		{
			name: "all identical start",
			input: []edit{
				{Start: 0, End: 3, New: "x"},
				{Start: 0, End: 3, New: "x"},
				{Start: 0, End: 3, New: "x"},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := deduplicateEdits(tt.input)
			if len(got) != tt.want {
				t.Errorf("deduplicateEdits returned %d edits, want %d", len(got), tt.want)
			}
		})
	}
}

func TestDeduplicateEdits_KeepsFirst(t *testing.T) {
	t.Parallel()

	edits := []edit{
		{Start: 5, End: 8, New: "first"},
		{Start: 5, End: 8, New: "first"},
	}
	got := deduplicateEdits(edits)
	if len(got) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(got))
	}
	if got[0].New != "first" {
		t.Errorf("expected first edit to be kept, got %q", got[0].New)
	}
}

func TestDeduplicateEdits_ConflictPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for conflicting edits, but none occurred")
		}
	}()
	deduplicateEdits([]edit{
		{Start: 0, End: 3, New: "a"},
		{Start: 0, End: 5, New: "b"},
	})
}

func TestApplyEdits_StableSort(t *testing.T) {
	t.Parallel()

	// Two edits at the same Start offset: SliceStable guarantees the first
	// one in the input slice wins (after dedup, only one survives, but if
	// both survive the order must be deterministic).
	src := "abcdef"
	edits := []edit{
		{Start: 0, End: 3, New: "FIRST"},
		{Start: 0, End: 3, New: "SECOND"},
		{Start: 4, End: 5, New: "X"},
	}
	// After stable sort, FIRST comes before SECOND; SECOND overlaps and is
	// skipped.
	got := applyEdits([]byte(src), edits)
	want := "FIRSTdXf"
	if got != want {
		t.Errorf("applyEdits stable sort: got %q, want %q", got, want)
	}
}

func TestApplyEdits_OutOfBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		src   string
		edits []edit
	}{
		{
			name:  "Start negative",
			src:   "hello",
			edits: []edit{{Start: -1, End: 3, New: "x"}},
		},
		{
			name:  "End before Start",
			src:   "hello",
			edits: []edit{{Start: 3, End: 1, New: "x"}},
		},
		{
			name:  "Start beyond source",
			src:   "hello",
			edits: []edit{{Start: 10, End: 12, New: "x"}},
		},
		{
			name:  "End beyond source",
			src:   "hello",
			edits: []edit{{Start: 0, End: 99, New: "x"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic for out-of-bounds edit, but none occurred")
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("expected string panic, got %T: %v", r, r)
				}
				if len(msg) == 0 {
					t.Fatal("panic message is empty")
				}
			}()
			applyEdits([]byte(tt.src), tt.edits)
		})
	}
}

func TestApplyEdits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		src   string
		edits []edit
		want  string
	}{
		{
			name:  "no edits",
			src:   "hello world",
			edits: nil,
			want:  "hello world",
		},
		{
			name: "single replacement",
			src:  "hello world",
			edits: []edit{
				{Start: 0, End: 5, New: "hi"},
			},
			want: "hi world",
		},
		{
			name: "multiple replacements",
			src:  "aaa bbb ccc",
			edits: []edit{
				{Start: 0, End: 3, New: "xxx"},
				{Start: 4, End: 7, New: "yyy"},
			},
			want: "xxx yyy ccc",
		},
		{
			name: "deletion",
			src:  "hello world",
			edits: []edit{
				{Start: 5, End: 11, New: ""},
			},
			want: "hello",
		},
		{
			name: "insertion via zero-width range",
			src:  "helloworld",
			edits: []edit{
				{Start: 5, End: 5, New: " "},
			},
			want: "hello world",
		},
		{
			name: "unsorted edits are applied in order",
			src:  "abcdef",
			edits: []edit{
				{Start: 4, End: 5, New: "X"},
				{Start: 0, End: 1, New: "Y"},
			},
			want: "YbcdXf",
		},
		{
			name: "overlapping edits — second skipped",
			src:  "abcdef",
			edits: []edit{
				{Start: 0, End: 4, New: "XY"},
				{Start: 2, End: 5, New: "ZZ"},
			},
			want: "XYef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := applyEdits([]byte(tt.src), tt.edits)
			if got != tt.want {
				t.Errorf("applyEdits(%q, ...) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}
