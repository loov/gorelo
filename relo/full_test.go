package relo_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

// TestFull runs a programmatically-generated fan-out of scenarios that
// combine each primary declaration kind (func, type, method, const, var)
// with each primary operation (rename, move, detach, attach, file-move,
// and their combinations) across modifier axes (same-pkg/cross-pkg,
// @stubs on/off, with/without consumer ref, with/without sibling ref).
//
// Every scenario must: compile without error, apply cleanly, and pass
// go vet on the resulting files. Skipped under -short because the matrix
// shells out to go vet once per scenario.
func TestFull(t *testing.T) {
	t.Parallel()

	scenarios := generateFullScenarios()
	t.Logf("generated %d scenarios", len(scenarios))

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			runFullScenario(t, sc)
		})
	}
}

// fullScenario describes a single generated scenario.
type fullScenario struct {
	name       string            // unique test name
	inputs     map[string]string // path -> content (relative to module root)
	rules      string            // rules file body
	checks     []fullCheck       // post-apply invariants
	skipVet    bool              // skip go vet (e.g., expected invalid output)
	expectWarn []string          // warnings whose substrings must appear (optional)
}

// fullCheck describes a post-apply invariant on a single output file.
type fullCheck struct {
	path     string
	exists   *bool    // nil = don't care; otherwise must/must-not exist
	contains []string // content must include each substring
	missing  []string // content must NOT include any of these substrings
}

func boolPtr(b bool) *bool { return &b }

// runFullScenario writes sc.inputs to a tmp dir, runs Compile, applies
// the plan, runs go vet, and asserts sc.checks.
func runFullScenario(t *testing.T, sc fullScenario) {
	t.Helper()
	tmp := t.TempDir()

	for rel, content := range sc.inputs {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ix, err := mast.Load(&mast.Config{Dir: tmp}, "./...")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	rf, err := rules.Parse("test", []byte(sc.rules))
	if err != nil {
		t.Fatalf("parsing rules: %v\n%s", err, sc.rules)
	}

	relos, fileMoves, err := relo.FromRules(ix, rf.Rules, tmp)
	if err != nil {
		t.Fatalf("FromRules: %v", err)
	}

	var opts relo.Options
	for _, d := range rf.Directives {
		if d.Key == "stubs" {
			opts.Stubs = true
		}
	}

	plan, err := relo.Compile(ix, relos, fileMoves, &opts)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for _, exp := range sc.expectWarn {
		found := false
		for _, w := range plan.Warnings {
			if strings.Contains(w.Message, exp) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning substring %q not found", exp)
		}
	}

	actual := make(map[string]string)
	for rel, content := range sc.inputs {
		if rel == "go.mod" {
			continue
		}
		actual[rel] = content
	}
	for _, fe := range plan.Edits {
		rel, err := filepath.Rel(tmp, fe.Path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		if fe.IsDelete {
			delete(actual, rel)
		} else {
			actual[rel] = fe.Content
		}
	}

	// Wipe .go files under tmp and rewrite from actual.
	_ = filepath.WalkDir(tmp, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			os.Remove(path)
		}
		return nil
	})
	for rel, content := range actual {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !sc.skipVet {
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir = tmp
		out, verr := cmd.CombinedOutput()
		if verr != nil && !strings.Contains(string(out), "matched no packages") {
			t.Errorf("go vet failed:\n%s\n\n---- output files ----\n%s", out, dumpActual(actual))
		}
	}

	for _, c := range sc.checks {
		content, exists := actual[c.path]
		if c.exists != nil {
			if *c.exists && !exists {
				t.Errorf("file %s expected to exist but does not\n---- output files ----\n%s",
					c.path, dumpActual(actual))
				continue
			}
			if !*c.exists && exists {
				t.Errorf("file %s expected to NOT exist, content:\n%s", c.path, content)
				continue
			}
		}
		if !exists {
			continue
		}
		for _, s := range c.contains {
			if !strings.Contains(content, s) {
				t.Errorf("file %s missing substring %q\nactual:\n%s", c.path, s, content)
			}
		}
		for _, s := range c.missing {
			if strings.Contains(content, s) {
				t.Errorf("file %s contains forbidden substring %q\nactual:\n%s", c.path, s, content)
			}
		}
	}
}

func dumpActual(actual map[string]string) string {
	var keys []string
	for k := range actual {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString("=== " + k + " ===\n")
		sb.WriteString(actual[k])
		if !strings.HasSuffix(actual[k], "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Scenario generator.
// ---------------------------------------------------------------------------

// modAxes describes independent modifier toggles applied to a base+op combo.
type modAxes struct {
	crossPkg bool
	stubs    bool
	consumer bool
	sibling  bool
}

// allAxes returns all 16 combinations of the four toggles. Callers filter
// out combinations that are invalid for a particular base+op.
func allAxes() []modAxes {
	var out []modAxes
	for _, cp := range []bool{false, true} {
		for _, st := range []bool{false, true} {
			for _, cn := range []bool{false, true} {
				for _, sb := range []bool{false, true} {
					out = append(out, modAxes{crossPkg: cp, stubs: st, consumer: cn, sibling: sb})
				}
			}
		}
	}
	return out
}

// axisName encodes the axis toggles into a short suffix.
func (a modAxes) suffix() string {
	parts := []string{"same"}
	if a.crossPkg {
		parts[0] = "cross"
	}
	if a.stubs {
		parts = append(parts, "stubs")
	}
	if a.consumer {
		parts = append(parts, "cons")
	}
	if a.sibling {
		parts = append(parts, "sib")
	}
	return strings.Join(parts, "-")
}

const fullModule = "example.com/full"

// goMod is the standard go.mod file shared by every scenario.
const goMod = "module " + fullModule + "\n\ngo 1.21\n"

func generateFullScenarios() []fullScenario {
	var scs []fullScenario
	scs = append(scs, genFuncScenarios()...)
	scs = append(scs, genTypeScenarios()...)
	scs = append(scs, genConstScenarios()...)
	scs = append(scs, genVarScenarios()...)
	scs = append(scs, genMethodScenarios()...)
	scs = append(scs, genAttachScenarios()...)
	scs = append(scs, genFileMoveScenarios()...)
	return scs
}

// ---------------------------------------------------------------------------
// Primary: Func.
// Ops: rename, move-same, move-cross, rename-move.
// ---------------------------------------------------------------------------

func genFuncScenarios() []fullScenario {
	var out []fullScenario

	// Op: rename. Same-package rename; crossPkg axis skipped (no cross move).
	for _, a := range allAxes() {
		if a.crossPkg || a.stubs {
			continue // rename-only is same-pkg, no stubs
		}
		name := "func-rename/" + a.suffix()
		inputs := map[string]string{
			"go.mod":     goMod,
			"src/src.go": "package src\n\nfunc Target() int { return 1 }\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = "package src\n\nfunc Sibling() int { return Target() }\n"
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = consumerCallFunc("Target")
		}
		checks := []fullCheck{
			{path: "src/src.go", contains: []string{"func Renamed()"}, missing: []string{"Target"}},
		}
		if a.sibling {
			checks = append(checks, fullCheck{path: "src/sibling.go", contains: []string{"Renamed()"}, missing: []string{"Target()"}})
		}
		if a.consumer {
			checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Renamed()"}})
		}
		out = append(out, fullScenario{
			name:   name,
			inputs: inputs,
			rules:  "Target=Renamed\n",
			checks: checks,
		})
	}

	// Op: move.
	for _, a := range allAxes() {
		if !a.crossPkg && a.stubs {
			continue // stubs only valid cross-pkg
		}
		name := "func-move/" + a.suffix()
		inputs := map[string]string{
			"go.mod":     goMod,
			"src/src.go": "package src\n\nfunc Target() int { return 1 }\n\nfunc Keep() int { return 2 }\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = "package src\n\nfunc Sibling() int { return Target() }\n"
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = consumerCallFunc("Target")
		}
		var rule string
		var targetPath string
		if a.crossPkg {
			rule = "Target -> dst/target.go"
			targetPath = "dst/target.go"
		} else {
			rule = "Target -> src/moved.go"
			targetPath = "src/moved.go"
		}
		body := rule + "\n"
		if a.stubs {
			body = "@stubs\n" + body
		}
		checks := []fullCheck{
			{path: targetPath, contains: []string{"func Target() int"}, exists: boolPtr(true)},
		}
		if !a.stubs {
			// Without stubs the original decl vanishes from src/src.go
			// (only Keep remains). With stubs a forwarding `func Target()`
			// stays behind.
			checks = append(checks, fullCheck{path: "src/src.go", missing: []string{"return 1"}})
		}
		if a.consumer {
			if a.crossPkg {
				if a.stubs {
					checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Target()"}})
				} else {
					checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Target()"}})
				}
			} else {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Target()"}})
			}
		}
		out = append(out, fullScenario{
			name:   name,
			inputs: inputs,
			rules:  body,
			checks: checks,
		})
	}

	// Op: move+rename.
	for _, a := range allAxes() {
		if !a.crossPkg && a.stubs {
			continue
		}
		name := "func-move-rename/" + a.suffix()
		inputs := map[string]string{
			"go.mod":     goMod,
			"src/src.go": "package src\n\nfunc Target() int { return 1 }\n\nfunc Keep() int { return 2 }\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = "package src\n\nfunc Sibling() int { return Target() }\n"
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = consumerCallFunc("Target")
		}
		var rule, targetPath string
		if a.crossPkg {
			rule = "Target=Renamed -> dst/target.go"
			targetPath = "dst/target.go"
		} else {
			rule = "Target=Renamed -> src/moved.go"
			targetPath = "src/moved.go"
		}
		body := rule + "\n"
		if a.stubs {
			body = "@stubs\n" + body
		}
		checks := []fullCheck{
			{path: targetPath, contains: []string{"func Renamed() int"}, exists: boolPtr(true)},
		}
		if a.consumer {
			if a.crossPkg {
				if a.stubs {
					checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Target()"}})
				} else {
					checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Renamed()"}})
				}
			} else {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Renamed()"}})
			}
		}
		out = append(out, fullScenario{
			name:   name,
			inputs: inputs,
			rules:  body,
			checks: checks,
		})
	}

	return out
}

func consumerCallFunc(fn string) string {
	return fmt.Sprintf(`package consumer

import "%s/src"

func Do() int { return src.%s() }
`, fullModule, fn)
}

// ---------------------------------------------------------------------------
// Primary: Type (exported struct with no methods).
// Ops: rename, move, move+rename.
// ---------------------------------------------------------------------------

func genTypeScenarios() []fullScenario {
	var out []fullScenario

	// Op: rename.
	for _, a := range allAxes() {
		if a.crossPkg || a.stubs {
			continue
		}
		name := "type-rename/" + a.suffix()
		inputs := map[string]string{
			"go.mod":     goMod,
			"src/src.go": "package src\n\ntype Target struct {\n\tAddr string\n}\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = "package src\n\nfunc Sibling() *Target { return &Target{Addr: \"x\"} }\n"
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = consumerUseType("Target")
		}
		checks := []fullCheck{
			{path: "src/src.go", contains: []string{"type Renamed struct"}},
		}
		if a.sibling {
			checks = append(checks, fullCheck{path: "src/sibling.go", contains: []string{"*Renamed", "&Renamed{"}})
		}
		if a.consumer {
			checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Renamed"}})
		}
		out = append(out, fullScenario{
			name:   name,
			inputs: inputs,
			rules:  "Target=Renamed\n",
			checks: checks,
		})
	}

	// Op: move.
	for _, a := range allAxes() {
		if !a.crossPkg && a.stubs {
			continue
		}
		name := "type-move/" + a.suffix()
		inputs := map[string]string{
			"go.mod":     goMod,
			"src/src.go": "package src\n\ntype Target struct {\n\tAddr string\n}\n\ntype Keep struct{}\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = "package src\n\nfunc Sibling() *Target { return &Target{Addr: \"x\"} }\n"
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = consumerUseType("Target")
		}
		var rule, targetPath string
		if a.crossPkg {
			rule = "Target -> dst/target.go"
			targetPath = "dst/target.go"
		} else {
			rule = "Target -> src/moved.go"
			targetPath = "src/moved.go"
		}
		body := rule + "\n"
		if a.stubs {
			body = "@stubs\n" + body
		}
		checks := []fullCheck{
			{path: targetPath, contains: []string{"type Target struct"}, exists: boolPtr(true)},
		}
		if a.consumer && a.crossPkg {
			if a.stubs {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Target"}})
			} else {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Target"}})
			}
		}
		out = append(out, fullScenario{
			name:   name,
			inputs: inputs,
			rules:  body,
			checks: checks,
		})
	}

	// Op: move+rename.
	for _, a := range allAxes() {
		if !a.crossPkg && a.stubs {
			continue
		}
		name := "type-move-rename/" + a.suffix()
		inputs := map[string]string{
			"go.mod":     goMod,
			"src/src.go": "package src\n\ntype Target struct {\n\tAddr string\n}\n\ntype Keep struct{}\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = "package src\n\nfunc Sibling() *Target { return &Target{Addr: \"x\"} }\n"
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = consumerUseType("Target")
		}
		var rule, targetPath string
		if a.crossPkg {
			rule = "Target=Renamed -> dst/target.go"
			targetPath = "dst/target.go"
		} else {
			rule = "Target=Renamed -> src/moved.go"
			targetPath = "src/moved.go"
		}
		body := rule + "\n"
		if a.stubs {
			body = "@stubs\n" + body
		}
		checks := []fullCheck{
			{path: targetPath, contains: []string{"type Renamed struct"}, exists: boolPtr(true)},
		}
		if a.consumer && a.crossPkg {
			if a.stubs {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Target"}})
			} else {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Renamed"}})
			}
		}
		out = append(out, fullScenario{
			name:   name,
			inputs: inputs,
			rules:  body,
			checks: checks,
		})
	}

	return out
}

func consumerUseType(typ string) string {
	return fmt.Sprintf(`package consumer

import "%s/src"

func Do() *src.%s { return &src.%s{Addr: "x"} }
`, fullModule, typ, typ)
}

// ---------------------------------------------------------------------------
// Primary: Const.
// ---------------------------------------------------------------------------

func genConstScenarios() []fullScenario {
	var out []fullScenario

	for _, op := range []string{"rename", "move", "move-rename"} {
		for _, a := range allAxes() {
			if op == "rename" && (a.crossPkg || a.stubs) {
				continue
			}
			if !a.crossPkg && a.stubs {
				continue
			}
			name := "const-" + op + "/" + a.suffix()
			inputs := map[string]string{
				"go.mod":     goMod,
				"src/src.go": "package src\n\nconst Target = 42\n",
			}
			if a.sibling {
				inputs["src/sibling.go"] = "package src\n\nfunc Sibling() int { return Target + 1 }\n"
			}
			if a.consumer {
				inputs["consumer/consumer.go"] = consumerReadConst("Target")
			}
			var rule, targetPath, newName string
			switch op {
			case "rename":
				rule = "Target=Renamed"
				newName = "Renamed"
			case "move":
				newName = "Target"
				if a.crossPkg {
					rule = "Target -> dst/target.go"
					targetPath = "dst/target.go"
				} else {
					rule = "Target -> src/moved.go"
					targetPath = "src/moved.go"
				}
			case "move-rename":
				newName = "Renamed"
				if a.crossPkg {
					rule = "Target=Renamed -> dst/target.go"
					targetPath = "dst/target.go"
				} else {
					rule = "Target=Renamed -> src/moved.go"
					targetPath = "src/moved.go"
				}
			}
			body := rule + "\n"
			if a.stubs {
				body = "@stubs\n" + body
			}
			var checks []fullCheck
			if op == "rename" {
				checks = append(checks, fullCheck{path: "src/src.go", contains: []string{"const Renamed"}, missing: []string{"Target"}})
				if a.consumer {
					checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Renamed"}})
				}
			} else {
				checks = append(checks, fullCheck{path: targetPath, contains: []string{"const " + newName}, exists: boolPtr(true)})
				if a.consumer && a.crossPkg {
					if a.stubs {
						checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Target"}})
					} else {
						checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst." + newName}})
					}
				}
			}
			out = append(out, fullScenario{name: name, inputs: inputs, rules: body, checks: checks})
		}
	}
	return out
}

func consumerReadConst(name string) string {
	return fmt.Sprintf(`package consumer

import "%s/src"

func Do() int { return src.%s }
`, fullModule, name)
}

// ---------------------------------------------------------------------------
// Primary: Var.
// ---------------------------------------------------------------------------

func genVarScenarios() []fullScenario {
	var out []fullScenario

	for _, op := range []string{"rename", "move", "move-rename"} {
		for _, a := range allAxes() {
			if op == "rename" && (a.crossPkg || a.stubs) {
				continue
			}
			if !a.crossPkg && a.stubs {
				continue
			}
			name := "var-" + op + "/" + a.suffix()
			inputs := map[string]string{
				"go.mod":     goMod,
				"src/src.go": "package src\n\nvar Target = 42\n",
			}
			if a.sibling {
				inputs["src/sibling.go"] = "package src\n\nfunc Sibling() int { return Target + 1 }\n"
			}
			if a.consumer {
				inputs["consumer/consumer.go"] = consumerReadConst("Target")
			}
			var rule, targetPath, newName string
			switch op {
			case "rename":
				rule = "Target=Renamed"
				newName = "Renamed"
			case "move":
				newName = "Target"
				if a.crossPkg {
					rule = "Target -> dst/target.go"
					targetPath = "dst/target.go"
				} else {
					rule = "Target -> src/moved.go"
					targetPath = "src/moved.go"
				}
			case "move-rename":
				newName = "Renamed"
				if a.crossPkg {
					rule = "Target=Renamed -> dst/target.go"
					targetPath = "dst/target.go"
				} else {
					rule = "Target=Renamed -> src/moved.go"
					targetPath = "src/moved.go"
				}
			}
			body := rule + "\n"
			if a.stubs {
				body = "@stubs\n" + body
			}
			var checks []fullCheck
			if op == "rename" {
				checks = append(checks, fullCheck{path: "src/src.go", contains: []string{"var Renamed"}})
			} else {
				checks = append(checks, fullCheck{path: targetPath, contains: []string{"var " + newName}, exists: boolPtr(true)})
			}
			out = append(out, fullScenario{name: name, inputs: inputs, rules: body, checks: checks})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Primary: Method (exercised via detach).
// Ops: rename, detach, detach+rename.
// ---------------------------------------------------------------------------

func genMethodScenarios() []fullScenario {
	var out []fullScenario

	// Op: detach (keep same name) same-pkg and cross-pkg.
	for _, a := range allAxes() {
		if !a.crossPkg && a.stubs {
			continue
		}
		// Stubs have no effect on detach — skip to cut the matrix.
		if a.stubs {
			continue
		}
		// Cross-pkg detach with a sibling call creates a legitimate
		// circular import: dst imports src for the receiver type and src
		// now imports dst to call the detached function. Skip this axis.
		if a.crossPkg && a.sibling {
			continue
		}
		name := "method-detach/" + a.suffix()
		inputs := buildMethodInputs(a)
		var rule string
		var checkFuncPath string
		if a.crossPkg {
			rule = "Server#Start=! -> dst/start.go"
			checkFuncPath = "dst/start.go"
		} else {
			rule = "Server#Start=!"
			checkFuncPath = "src/src.go"
		}
		checks := []fullCheck{
			{path: checkFuncPath, contains: []string{"func Start("}, exists: boolPtr(true)},
		}
		if a.consumer {
			if a.crossPkg {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Start("}})
			} else {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Start("}})
			}
		}
		out = append(out, fullScenario{name: name, inputs: inputs, rules: rule + "\n", checks: checks})
	}

	// Op: detach+rename.
	for _, a := range allAxes() {
		if a.stubs {
			continue // stubs irrelevant for detach
		}
		if a.crossPkg && a.sibling {
			continue // circular import (see method-detach above)
		}
		name := "method-detach-rename/" + a.suffix()
		inputs := buildMethodInputs(a)
		var rule, checkFuncPath string
		if a.crossPkg {
			rule = "Server#Start=!Begin -> dst/start.go"
			checkFuncPath = "dst/start.go"
		} else {
			rule = "Server#Start=!Begin"
			checkFuncPath = "src/src.go"
		}
		checks := []fullCheck{
			{path: checkFuncPath, contains: []string{"func Begin("}, exists: boolPtr(true)},
		}
		if a.consumer {
			if a.crossPkg {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Begin("}})
			} else {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"src.Begin("}})
			}
		}
		out = append(out, fullScenario{name: name, inputs: inputs, rules: rule + "\n", checks: checks})
	}

	// Op: method rename only (same-pkg, since cross-pkg rename of a method
	// doesn't move the method — it stays attached to the type).
	for _, a := range allAxes() {
		if a.crossPkg || a.stubs {
			continue
		}
		name := "method-rename/" + a.suffix()
		inputs := buildMethodInputs(a)
		rule := "Server#Start=Begin"
		checks := []fullCheck{
			{path: "src/src.go", contains: []string{"func (s *Server) Begin("}},
		}
		if a.consumer {
			checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{".Begin()"}})
		}
		out = append(out, fullScenario{name: name, inputs: inputs, rules: rule + "\n", checks: checks})
	}

	return out
}

func buildMethodInputs(a modAxes) map[string]string {
	inputs := map[string]string{
		"go.mod": goMod,
		"src/src.go": `package src

type Server struct {
	Addr string
}

func (s *Server) Start() error {
	return nil
}
`,
	}
	if a.sibling {
		inputs["src/sibling.go"] = `package src

func Sibling(s *Server) error {
	return s.Start()
}
`
	}
	if a.consumer {
		inputs["consumer/consumer.go"] = fmt.Sprintf(`package consumer

import "%s/src"

func Do() error {
	s := &src.Server{Addr: "x"}
	return s.Start()
}
`, fullModule)
	}
	return inputs
}

// ---------------------------------------------------------------------------
// Primary: Attach (func becoming a method).
// Ops: attach, attach+rename.
// ---------------------------------------------------------------------------

func genAttachScenarios() []fullScenario {
	var out []fullScenario

	for _, op := range []string{"attach", "attach-rename"} {
		for _, a := range allAxes() {
			if a.stubs && !a.crossPkg {
				continue
			}
			// Attach always needs the receiver type defined; siblings
			// optional, consumer optional. Stubs valid cross-pkg but
			// suppressed (kind change), so keep exercising it.
			name := "func-" + op + "/" + a.suffix()
			inputs := buildAttachInputs(a)
			var rule string
			var methodName string
			switch op {
			case "attach":
				methodName = "Start"
				if a.crossPkg {
					rule = "Start=Server#Start -> srv/methods.go"
				} else {
					rule = "Start=Server#Start"
				}
			case "attach-rename":
				methodName = "Boot"
				if a.crossPkg {
					rule = "Start=Server#Boot -> srv/methods.go"
				} else {
					rule = "Start=Server#Boot"
				}
			}
			body := rule + "\n"
			if a.stubs {
				body = "@stubs\n" + body
			}
			var methodFile string
			if a.crossPkg {
				methodFile = "srv/methods.go"
			} else {
				methodFile = "src/src.go"
			}
			checks := []fullCheck{
				{path: methodFile, contains: []string{"func (s *Server) " + methodName + "("}, exists: boolPtr(true)},
			}
			if a.consumer {
				checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"." + methodName + "("}})
			}
			out = append(out, fullScenario{name: name, inputs: inputs, rules: body, checks: checks})
		}
	}
	return out
}

func buildAttachInputs(a modAxes) map[string]string {
	if a.crossPkg {
		inputs := map[string]string{
			"go.mod": goMod,
			"src/src.go": fmt.Sprintf(`package src

import "%s/srv"

func Start(s *srv.Server) error {
	return nil
}
`, fullModule),
			"srv/server.go": "package srv\n\ntype Server struct{}\n",
		}
		if a.sibling {
			inputs["src/sibling.go"] = fmt.Sprintf(`package src

import "%s/srv"

func Sibling(s *srv.Server) error {
	return Start(s)
}
`, fullModule)
		}
		if a.consumer {
			inputs["consumer/consumer.go"] = fmt.Sprintf(`package consumer

import (
	"%s/src"
	"%s/srv"
)

func Do() error {
	s := &srv.Server{}
	return src.Start(s)
}
`, fullModule, fullModule)
		}
		return inputs
	}

	inputs := map[string]string{
		"go.mod": goMod,
		"src/src.go": `package src

type Server struct{}

func Start(s *Server) error {
	return nil
}
`,
	}
	if a.sibling {
		inputs["src/sibling.go"] = `package src

func Sibling(s *Server) error {
	return Start(s)
}
`
	}
	if a.consumer {
		inputs["consumer/consumer.go"] = fmt.Sprintf(`package consumer

import "%s/src"

func Do() error {
	s := &src.Server{}
	return src.Start(s)
}
`, fullModule)
	}
	return inputs
}

// ---------------------------------------------------------------------------
// Primary: FileMove.
// Ops: bare, +rename, +detach, +attach.
// ---------------------------------------------------------------------------

func genFileMoveScenarios() []fullScenario {
	var out []fullScenario

	// Bare file-move (always cross-pkg in spirit; a same-pkg file move
	// within one package is also supported but less interesting).
	for _, a := range allAxes() {
		if a.stubs { // @stubs with_ignored.txtar already tests suppression
			continue
		}
		if !a.crossPkg { // keep matrix small: only cross-pkg file moves
			continue
		}
		name := "filemove/" + a.suffix()
		inputs := buildFileMoveInputs(a)
		rule := "src/server.go -> dst/server.go\n"
		checks := []fullCheck{
			{path: "dst/server.go", contains: []string{"type Server struct", "func (s *Server) Start("}, exists: boolPtr(true)},
			{path: "src/server.go", exists: boolPtr(false)},
		}
		if a.sibling {
			checks = append(checks, fullCheck{path: "src/sibling.go", contains: []string{"dst.Server", "s.Start()"}})
		}
		if a.consumer {
			checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Server", "s.Start()"}})
		}
		out = append(out, fullScenario{name: name, inputs: inputs, rules: rule, checks: checks})
	}

	// File-move + rename of a decl inside.
	for _, a := range allAxes() {
		if a.stubs || !a.crossPkg {
			continue
		}
		name := "filemove-rename/" + a.suffix()
		inputs := buildFileMoveInputs(a)
		rule := "src/server.go -> dst/server.go\nServer=Host\n"
		checks := []fullCheck{
			{path: "dst/server.go", contains: []string{"type Host struct", "func (s *Host) Start("}, exists: boolPtr(true)},
		}
		if a.consumer {
			checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Host", "s.Start()"}})
		}
		out = append(out, fullScenario{name: name, inputs: inputs, rules: rule, checks: checks})
	}

	// File-move + detach of a method inside.
	for _, a := range allAxes() {
		if a.stubs || !a.crossPkg {
			continue
		}
		name := "filemove-detach/" + a.suffix()
		inputs := buildFileMoveInputs(a)
		rule := "src/server.go -> dst/server.go\nServer#Start=!\n"
		checks := []fullCheck{
			{path: "dst/server.go", contains: []string{"type Server struct", "func Start(s *Server)"}, exists: boolPtr(true)},
		}
		if a.consumer {
			checks = append(checks, fullCheck{path: "consumer/consumer.go", contains: []string{"dst.Start("}})
		}
		out = append(out, fullScenario{name: name, inputs: inputs, rules: rule, checks: checks})
	}

	return out
}

func buildFileMoveInputs(a modAxes) map[string]string {
	inputs := map[string]string{
		"go.mod": goMod,
		"src/server.go": `package src

type Server struct {
	Addr string
}

func (s *Server) Start() error {
	return nil
}
`,
	}
	if a.sibling {
		inputs["src/sibling.go"] = `package src

func Sibling() error {
	s := &Server{Addr: "x"}
	return s.Start()
}
`
	}
	if a.consumer {
		inputs["consumer/consumer.go"] = fmt.Sprintf(`package consumer

import "%s/src"

func Do() error {
	s := &src.Server{Addr: "x"}
	return s.Start()
}
`, fullModule)
	}
	return inputs
}
