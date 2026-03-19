package relo_test

import (
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
)

func TestGolden(t *testing.T) {
	entries, err := filepath.Glob("testdata/*.txtar")
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := strings.TrimSuffix(filepath.Base(entry), ".txtar")
		t.Run(name, func(t *testing.T) {
			runGoldenTest(t, entry)
		})
	}
}

// runGoldenTest runs a single txtar-based golden test.
//
// The txtar comment section contains relo instructions (same format as before):
//
//	Name -> target.go
//	Name => NewName
//	Name -> target.go => NewName
//
// Lines starting with # are comments.
//
// The archive files are split by prefix:
//   - input/*  — Go source files to load
//   - golden/* — expected output files after applying the plan
func runGoldenTest(t *testing.T, txtarPath string) {
	t.Helper()

	data, err := os.ReadFile(txtarPath)
	if err != nil {
		t.Fatal("reading txtar:", err)
	}
	ar := txtar.Parse(data)

	// Split files into input and golden maps.
	inputFiles := make(map[string][]byte)
	goldenFiles := make(map[string]string)
	for _, f := range ar.Files {
		switch {
		case strings.HasPrefix(f.Name, "input/"):
			inputFiles[strings.TrimPrefix(f.Name, "input/")] = f.Data
		case strings.HasPrefix(f.Name, "golden/"):
			goldenFiles[strings.TrimPrefix(f.Name, "golden/")] = string(f.Data)
		default:
			t.Fatalf("unexpected file in txtar: %s (must start with input/ or golden/)", f.Name)
		}
	}

	// Write input files to a temp directory.
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "pkg")
	for name, content := range inputFiles {
		path := filepath.Join(pkgDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Load all packages in the test module.
	ix, err := mast.Load(&mast.Config{Dir: pkgDir}, "./...")
	if err != nil {
		t.Fatal("loading package:", err)
	}

	// Parse relo instructions from the txtar comment.
	relos := parseReloLines(t, string(ar.Comment), ix, pkgDir)

	// Compile.
	plan, err := relo.Compile(ix, relos, nil)
	if err != nil {
		t.Fatal("compile:", err)
	}

	// Check warnings against expected list.
	// Lines starting with "warn:" in the txtar comment are expected warnings.
	var expectedWarnings []string
	for _, line := range strings.Split(string(ar.Comment), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "warn:") {
			expectedWarnings = append(expectedWarnings, strings.TrimSpace(strings.TrimPrefix(line, "warn:")))
		}
	}
	var actualWarnings []string
	for _, w := range plan.Warnings {
		actualWarnings = append(actualWarnings, w.Message)
	}
	// Each expected warning must be a substring of some actual warning.
	for _, exp := range expectedWarnings {
		found := false
		for _, act := range actualWarnings {
			if strings.Contains(act, exp) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning containing %q not found", exp)
		}
	}
	// Each actual warning must match some expected warning.
	for _, act := range actualWarnings {
		found := false
		for _, exp := range expectedWarnings {
			if strings.Contains(act, exp) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected warning: %s", act)
		}
	}

	// Build actual output: start with input files, apply edits.
	actual := make(map[string]string)
	for name, content := range inputFiles {
		if name == "go.mod" {
			continue
		}
		actual[name] = string(content)
	}
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

	// Compare actual vs golden.
	allKeys := make(map[string]bool)
	for k := range goldenFiles {
		allKeys[k] = true
	}
	for k := range actual {
		allKeys[k] = true
	}

	sorted := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		exp, hasExp := goldenFiles[k]
		act, hasAct := actual[k]
		if hasExp && !hasAct {
			t.Errorf("missing file %s in output", k)
			continue
		}
		if !hasExp && hasAct {
			t.Errorf("unexpected file %s in output:\n%s", k, act)
			continue
		}
		expNorm := strings.TrimRight(exp, "\n\r\t ")
		actNorm := strings.TrimRight(act, "\n\r\t ")
		if expNorm != actNorm {
			t.Errorf("file %s differs:\n--- expected ---\n%s\n--- actual ---\n%s", k, exp, act)
		}
	}
}

// parseReloLines parses relo instructions from text (the txtar comment section).
func parseReloLines(t *testing.T, text string, ix *mast.Index, pkgDir string) []relo.Relo {
	t.Helper()
	var relos []relo.Relo
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "warn:") {
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
