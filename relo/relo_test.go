package relo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

func TestGolden(t *testing.T) {
	t.Parallel()

	var entries []string
	err := filepath.WalkDir("testdata", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".txtar") {
			entries = append(entries, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := strings.TrimPrefix(entry, "testdata"+string(filepath.Separator))
		name = strings.TrimSuffix(name, ".txtar")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runGoldenTest(t, entry)
		})
	}
}

// runGoldenTest runs a single txtar-based golden test.
//
// The txtar comment section contains rules in the same format as gorelo
// rules files (parsed by rules.Parse):
//
//	Name -> target.go          # move
//	Name=NewName               # rename only
//	Name=NewName -> target.go  # move and rename
//	Type#Field=NewField        # field rename
//
// Lines starting with "warn:" declare expected warnings.
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

	// Parse rules and relo instructions from the txtar comment.
	f, relos, resolveErr := parseRules(t, string(ar.Comment), ix, pkgDir)

	// Build compile options from directives.
	var opts relo.Options
	for _, d := range f.Directives {
		switch d.Key {
		case "stubs":
			opts.Stubs = true
		}
	}

	// Check for @error directive — if present, expect an error.
	var expectedError string
	for _, d := range f.Directives {
		if d.Key == "error" {
			expectedError = d.Value
		}
	}
	if expectedError != "" {
		combinedErr := resolveErr
		var compileErr error
		if combinedErr == nil {
			_, compileErr = relo.Compile(ix, relos, &opts)
			combinedErr = compileErr
		}
		if combinedErr == nil {
			t.Fatalf("expected error containing %q, but got no error", expectedError)
		}
		if !strings.Contains(combinedErr.Error(), expectedError) {
			t.Fatalf("expected error containing %q, got: %s", expectedError, combinedErr)
		}
		return
	}
	if resolveErr != nil {
		t.Fatal("resolving rules:", resolveErr)
	}

	// Compile.
	plan, err := relo.Compile(ix, relos, &opts)
	if err != nil {
		t.Fatal("compile:", err)
	}

	// Check warnings against expected list from @warn directives.
	var expectedWarnings []string
	for _, d := range f.Directives {
		if d.Key == "warn" {
			expectedWarnings = append(expectedWarnings, d.Value)
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
		relPath = filepath.ToSlash(relPath)
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
			t.Errorf("file %s differs:\n%s", k, lineDiff(expNorm, actNorm))
		}
	}

	// Run go vet on the output to catch invalid code.
	var expectedVet []string
	for _, d := range f.Directives {
		if d.Key == "vet" {
			expectedVet = append(expectedVet, d.Value)
		}
	}

	// Write actual output files to the temp directory.
	// First remove all .go files, then write the output.
	_ = filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			os.Remove(path)
		}
		return nil
	})
	for name, content := range actual {
		path := filepath.Join(pkgDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = pkgDir
	vetOut, vetErr := cmd.CombinedOutput()
	vetOutput := string(vetOut)

	if len(expectedVet) == 0 {
		// No @vet directives: vet must pass.
		if vetErr != nil {
			t.Errorf("go vet failed on output:\n%s", vetOutput)
		}
	} else {
		// @vet directives present: vet output must contain each.
		if vetErr == nil {
			t.Errorf("expected go vet to fail, but it passed")
		}
		for _, exp := range expectedVet {
			if !strings.Contains(vetOutput, exp) {
				t.Errorf("expected go vet output containing %q, got:\n%s", exp, vetOutput)
			}
		}
	}
}

// lineDiff produces a unified-style diff between two strings.
// Carriage returns are rendered as literal \r so that CRLF
// vs LF differences are visible in test output.
func lineDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	var b strings.Builder
	b.WriteString("--- want\n+++ got\n")

	// Simple line-by-line comparison showing context around changes.
	max := len(wantLines)
	if len(gotLines) > max {
		max = len(gotLines)
	}
	for i := 0; i < max; i++ {
		var wl, gl string
		haveW := i < len(wantLines)
		haveG := i < len(gotLines)
		if haveW {
			wl = showCR(wantLines[i])
		}
		if haveG {
			gl = showCR(gotLines[i])
		}
		switch {
		case haveW && haveG && wl == gl:
			b.WriteString("  " + wl + "\n")
		case haveW && haveG:
			b.WriteString("- " + wl + "\n")
			b.WriteString("+ " + gl + "\n")
		case haveW:
			b.WriteString("- " + wl + "\n")
		case haveG:
			b.WriteString("+ " + gl + "\n")
		}
	}
	return b.String()
}

// showCR replaces \r with the literal string \r so it is visible.
func showCR(s string) string {
	return strings.ReplaceAll(s, "\r", `\r`)
}

// parseRules parses the txtar comment section using rules.Parse and
// resolves items to relo.Relo via relo.FromRules. Returns the parsed
// file (for directive access) and the resolved relos.
func parseRules(t *testing.T, text string, ix *mast.Index, pkgDir string) (*rules.File, []relo.Relo, error) {
	t.Helper()

	f, err := rules.Parse("test", []byte(text))
	if err != nil {
		t.Fatal("parsing rules:", err)
	}

	relos, err := relo.FromRules(ix, f.Rules, pkgDir)
	if err != nil {
		return f, nil, err
	}
	return f, relos, nil
}
