package relo

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNoRawIdentityReadsInRewriters enforces that rewrite-phase files
// (detach.go and assemble.go) do not recompute "post-operation identity"
// directly. They must go through the helpers in finalloc.go so that
// concurrent renames and moves from other relos in the same run are
// reflected consistently. Historical bugs bc45f06 and 7d8c120 both came
// from ad-hoc reads of source-side state in rewrite contexts.
//
// Whitelisted patterns are legitimate uses (e.g. reading a length for an
// edit-range byte offset, or reading an import alias) and are excluded
// by regex.
func TestNoRawIdentityReadsInRewriters(t *testing.T) {
	files := []string{"detach.go", "assemble.go"}

	// Each rule has:
	//   - a name for the diagnostic
	//   - a regex that matches the banned pattern
	//   - an optional exemption regex applied per match line
	//   - a hint pointing at the preferred helper
	type rule struct {
		name   string
		banned *regexp.Regexp
		exempt *regexp.Regexp
		hint   string
	}
	rules := []rule{
		{
			name:   "filepath.Dir(rr.TargetFile)",
			banned: regexp.MustCompile(`filepath\.Dir\(rr\.TargetFile\)`),
			hint:   "use finalDir(rr) from finalloc.go",
		},
		{
			name:   "guessImportPath(filepath.Dir(rr.TargetFile))",
			banned: regexp.MustCompile(`guessImportPath\(filepath\.Dir\(rr\.TargetFile\)\)`),
			hint:   "use finalImportPath(rr) from finalloc.go",
		},
		{
			name:   "rr.Group.Pkg direct read",
			banned: regexp.MustCompile(`\brr\.Group\.Pkg\b`),
			hint:   "use finalImportPathForGroup(resolved, rr.Group) or finalDirForGroup — rr.Group.Pkg is always the PRE-move path",
		},
	}

	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			for _, r := range rules {
				if !r.banned.MatchString(line) {
					continue
				}
				if r.exempt != nil && r.exempt.MatchString(line) {
					continue
				}
				t.Errorf("%s:%d: banned pattern %q — %s\n\t%s",
					name, i+1, r.name, r.hint, strings.TrimSpace(line))
			}
		}
	}
}
