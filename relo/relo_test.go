package relo_test

import (
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
)

func TestRewriteConsumersNotImplemented(t *testing.T) {
	_, err := relo.Compile(nil, nil, &relo.Options{RewriteConsumers: true})
	if err == nil {
		t.Fatal("expected error when RewriteConsumers is true, got nil")
	}
	want := "RewriteConsumers is not yet implemented"
	if err.Error() != want {
		t.Fatalf("got error %q, want %q", err.Error(), want)
	}
}

func TestGolden(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			runGoldenTest(t, filepath.Join("testdata", name))
		})
	}
}

// runGoldenTest runs a single golden-file test case.
//
// Each test directory contains:
//   - input/      — Go source files to load
//   - golden/     — expected output files after applying the plan
//   - relo.txt    — relo instructions, one per line: "Name -> target.go" or "Name => NewName" or "Name -> target.go => NewName"
func runGoldenTest(t *testing.T, testDir string) {
	t.Helper()

	inputDir := filepath.Join(testDir, "input")
	goldenDir := filepath.Join(testDir, "golden")

	// Copy input files to a temp directory so we can verify output.
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "pkg")
	copyDir(t, inputDir, pkgDir)

	// Load the package.
	ix, err := mast.Load(&mast.Config{Dir: pkgDir}, ".")
	if err != nil {
		t.Fatal("loading package:", err)
	}

	// Parse relo instructions.
	reloFile := filepath.Join(testDir, "relo.txt")
	relos := parseReloFile(t, reloFile, ix, pkgDir)

	// Compile.
	plan, err := relo.Compile(ix, relos, nil)
	if err != nil {
		t.Fatal("compile:", err)
	}

	// Log warnings.
	for _, w := range plan.Warnings {
		t.Log("warning:", w.Message)
	}

	// Verify edits match golden files.
	// Build a map of expected files from golden dir.
	expected := readGoldenDir(t, goldenDir)

	// Build a map of result files from the plan.
	// Start with input files, apply edits. Exclude go.mod from comparison.
	actual := readDir(t, pkgDir)
	delete(actual, "go.mod")
	for _, fe := range plan.Edits {
		relPath, err := filepath.Rel(pkgDir, fe.Path)
		if err != nil {
			t.Fatal(err)
		}
		if fe.IsDelete {
			delete(actual, relPath)
		} else {
			actual[relPath] = fe.Content
		}
	}

	// Compare.
	allKeys := make(map[string]bool)
	for k := range expected {
		allKeys[k] = true
	}
	for k := range actual {
		allKeys[k] = true
	}

	var sortedAllKeys []string
	for k := range allKeys {
		sortedAllKeys = append(sortedAllKeys, k)
	}
	sort.Strings(sortedAllKeys)

	for _, k := range sortedAllKeys {
		exp, hasExp := expected[k]
		act, hasAct := actual[k]
		if hasExp && !hasAct {
			t.Errorf("missing file %s in output", k)
			continue
		}
		if !hasExp && hasAct {
			t.Errorf("unexpected file %s in output:\n%s", k, act)
			continue
		}
		// Normalize trailing whitespace for comparison.
		expNorm := strings.TrimRight(exp, "\n\r\t ")
		actNorm := strings.TrimRight(act, "\n\r\t ")
		if expNorm != actNorm {
			t.Errorf("file %s differs:\n--- expected ---\n%s\n--- actual ---\n%s", k, exp, act)
		}
	}
}

// parseReloFile parses relo instructions from a file.
// Format: "Name -> target.go" for move, "Name => NewName" for rename,
// "Name -> target.go => NewName" for both.
func parseReloFile(t *testing.T, path string, ix *mast.Index, pkgDir string) []relo.Relo {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("reading relo file:", err)
	}

	var relos []relo.Relo
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var name, moveTo, rename string

		if idx := strings.Index(line, " -> "); idx >= 0 {
			name = strings.TrimSpace(line[:idx])
			rest := strings.TrimSpace(line[idx+4:])
			if idx2 := strings.Index(rest, " => "); idx2 >= 0 {
				moveTo = strings.TrimSpace(rest[:idx2])
				rename = strings.TrimSpace(rest[idx2+4:])
			} else {
				moveTo = rest
			}
		} else if idx := strings.Index(line, " => "); idx >= 0 {
			name = strings.TrimSpace(line[:idx])
			rename = strings.TrimSpace(line[idx+4:])
		} else {
			t.Fatalf("invalid relo line: %q", line)
		}

		// Find the definition ident.
		ident := findDefIdent(t, ix, name)

		r := relo.Relo{Ident: ident}
		if moveTo != "" {
			r.MoveTo = filepath.Join(pkgDir, moveTo)
		}
		if rename != "" {
			r.Rename = rename
		}
		relos = append(relos, r)
	}
	return relos
}

// findDefIdent finds a definition *ast.Ident with the given name.
func findDefIdent(t *testing.T, ix *mast.Index, name string) *ast.Ident {
	t.Helper()
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Syntax.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Name.Name == name {
						return d.Name
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.Name == name {
								return s.Name
							}
						case *ast.ValueSpec:
							for _, n := range s.Names {
								if n.Name == name {
									return n
								}
							}
						}
					}
				}
			}
		}
	}
	t.Fatalf("definition ident %q not found", name)
	return nil
}

// copyDir copies all files from src to dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatal("copying dir:", err)
	}
}

// readDir reads all files in a directory into a map of relative path -> content.
func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		result[rel] = string(data)
		return nil
	})
	return result
}

// readGoldenDir reads all files in a golden directory.
func readGoldenDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("golden directory does not exist:", dir)
	}
	return readDir(t, dir)
}
