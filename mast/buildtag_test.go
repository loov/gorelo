package mast

import "testing"

func TestExtractBuildTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		src  string
		want string
	}{
		{
			name: "go:build directive",
			path: "foo.go",
			src:  "//go:build linux\n\npackage p\n",
			want: "linux",
		},
		{
			name: "no constraint",
			path: "foo.go",
			src:  "package p\n",
			want: "",
		},
		{
			name: "filename GOOS",
			path: "foo_linux.go",
			src:  "package p\n",
			want: "linux",
		},
		{
			name: "filename GOARCH",
			path: "foo_amd64.go",
			src:  "package p\n",
			want: "amd64",
		},
		{
			name: "filename GOOS test",
			path: "foo_linux_test.go",
			src:  "package p\n",
			want: "linux",
		},
		{
			name: "filename GOARCH test",
			path: "foo_amd64_test.go",
			src:  "package p\n",
			want: "amd64",
		},
		{
			name: "directive takes precedence over filename",
			path: "foo_linux.go",
			src:  "//go:build darwin\n\npackage p\n",
			want: "darwin",
		},
		{
			name: "plain test file",
			path: "foo_test.go",
			src:  "package p\n",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := extractBuildTag(tt.path, []byte(tt.src))
			if got != tt.want {
				t.Errorf("extractBuildTag(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
