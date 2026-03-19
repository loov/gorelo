package mast

import (
	"path/filepath"
	"testing"
)

func TestFileMatchesSource(t *testing.T) {
	t.Parallel()

	sep := string(filepath.Separator)

	tests := []struct {
		name     string
		filePath string
		source   string
		want     bool
	}{
		{
			name:     "exact match",
			filePath: "bar.go",
			source:   "bar.go",
			want:     true,
		},
		{
			name:     "suffix with separator",
			filePath: sep + "project" + sep + "bar.go",
			source:   "bar.go",
			want:     true,
		},
		{
			name:     "partial filename no match",
			filePath: sep + "project" + sep + "foobar.go",
			source:   "bar.go",
			want:     false,
		},
		{
			name:     "subdirectory match",
			filePath: sep + "project" + sep + "sub" + sep + "file.go",
			source:   "sub/file.go",
			want:     true,
		},
		{
			name:     "wrong directory",
			filePath: sep + "project" + sep + "other" + sep + "file.go",
			source:   "sub/file.go",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := fileMatchesSource(tt.filePath, tt.source)
			if got != tt.want {
				t.Errorf("fileMatchesSource(%q, %q) = %v, want %v",
					tt.filePath, tt.source, got, tt.want)
			}
		})
	}
}
