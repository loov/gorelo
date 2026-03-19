package relo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuessImportLocalName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"fmt", "fmt"},
		{"math/rand", "rand"},
		{"crypto/rand", "rand"},
		{"encoding/json", "json"},
		{"github.com/foo/bar", "bar"},
		{"github.com/foo/bar/v2", "bar"},           // versioned
		{"github.com/foo/bar/v3", "bar"},           // versioned
		{"github.com/mattn/go-ieproxy", "ieproxy"}, // go- prefix stripped
		{"github.com/foo/go_bar", "bar"},           // go_ prefix stripped
		{"github.com/foo/my-lib", "mylib"},         // dash removed
		{"github.com/foo/my.lib", "mylib"},         // dot removed
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := guessImportLocalName(tt.path)
			if got != tt.want {
				t.Errorf("guessImportLocalName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestParentPrefixedName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"math/rand", "mathrand"},
		{"crypto/rand", "cryptorand"},
		{"encoding/json", "encodingjson"},
		{"fmt", "fmt"}, // no parent
		{"github.com/foo/bar", "foobar"},
		{"github.com/foo-bar/baz", "foobar" + "baz"}, // dash in parent removed
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := parentPrefixedName(tt.path)
			if got != tt.want {
				t.Errorf("parentPrefixedName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestGuessImportPath(t *testing.T) {
	// Create a temp directory with a go.mod.
	dir := t.TempDir()
	modContent := "module example.com/mymod\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Root directory.
	got := guessImportPath(dir)
	if got != "example.com/mymod" {
		t.Errorf("guessImportPath(root) = %q, want %q", got, "example.com/mymod")
	}

	// Subdirectory.
	subDir := filepath.Join(dir, "sub", "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	got = guessImportPath(subDir)
	if got != "example.com/mymod/sub/pkg" {
		t.Errorf("guessImportPath(sub/pkg) = %q, want %q", got, "example.com/mymod/sub/pkg")
	}
}

func TestGuessImportPath_NoGoMod(t *testing.T) {
	dir := t.TempDir()
	got := guessImportPath(dir)
	if got != "" {
		t.Errorf("guessImportPath(no go.mod) = %q, want empty", got)
	}
}

func TestReadModulePath(t *testing.T) {
	dir := t.TempDir()

	// No go.mod.
	got := readModulePath(dir)
	if got != "" {
		t.Errorf("readModulePath(no go.mod) = %q, want empty", got)
	}

	// With go.mod.
	modContent := "module example.com/mymod\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}
	got = readModulePath(dir)
	if got != "example.com/mymod" {
		t.Errorf("readModulePath = %q, want %q", got, "example.com/mymod")
	}
}
