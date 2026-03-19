package main

import (
	"path/filepath"
	"testing"
)

func TestRelativePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		base, full string
		want       string
	}{
		{"/proj", "/proj/src/a.go", "src/a.go"},
		{"/proj", "/proj/a.go", "a.go"},
		{"/proj/pkg", "/proj/pkg/sub/b.go", "sub/b.go"},
	}
	for _, tt := range tests {
		base := filepath.FromSlash(tt.base)
		full := filepath.FromSlash(tt.full)
		got := relativePath(base, full)
		if got != tt.want {
			t.Errorf("relativePath(%q, %q) = %q, want %q", base, full, got, tt.want)
		}
	}
}
