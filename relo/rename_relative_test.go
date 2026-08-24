package relo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
)

func TestRenameOnly_RelativeSourcePathDoesNotBecomeMove(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/repro\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "repro.go"), "package repro\n\nfunc ContentAdaptive() int { return 42 }\n")
	write(t, filepath.Join(dir, "repro_test.go"), `package repro

import "testing"

func TestContentAdaptive(t *testing.T) {
	if ContentAdaptive() != 42 { t.Fatal("wrong result") }
}
`)
	t.Chdir(dir)

	ix, err := mast.Load(&mast.Config{Dir: "."}, ".")
	if err != nil {
		t.Fatal(err)
	}
	// Model the relative file paths present in the failing CLI invocation.
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			delete(ix.FilesByPath, file.Path)
			file.Path = filepath.Base(file.Path)
			ix.FilesByPath[file.Path] = file
		}
	}

	def := ix.FindDef("ContentAdaptive", "")
	plan, err := relo.Compile(ix, []relo.Relo{{Ident: def, Rename: "Resize"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := relo.Apply(plan); err != nil {
		t.Fatal(err)
	}

	source, err := os.ReadFile("repro.go")
	if err != nil {
		t.Errorf("rename-only operation removed repro.go: %v", err)
	} else if !strings.Contains(string(source), "func Resize()") {
		t.Errorf("renamed declaration missing from repro.go:\n%s", source)
	}
	testSource, err := os.ReadFile("repro_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(testSource), `"example.com/repro"`) {
		t.Errorf("same-package test gained a self-import:\n%s", testSource)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
