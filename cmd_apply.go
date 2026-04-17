package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"

	"github.com/zeebo/clingy"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

type cmdApply struct {
	args       []string
	verbose    bool
	cpuprofile string
	dryRun     bool
}

func (c *cmdApply) Setup(params clingy.Parameters) {
	c.verbose = params.Flag("verbose", "print each file edit to stderr", false,
		clingy.Short('v'), clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.cpuprofile = params.Flag("cpuprofile", "write CPU profile to file", "").(string)
	c.args = params.Arg("rule-or-file", "rule string or path to a .rules file",
		clingy.Repeated, clingy.Optional).([]string)
}

func (c *cmdApply) Execute(ctx context.Context) error {
	ruleFiles, inlineRules := classifyArgs(c.args)
	defaultFile := len(c.args) == 0
	if defaultFile {
		ruleFiles = []string{"gorelo.rules"}
	}
	return withProfile(c.cpuprofile, func() error {
		return runRelo(c.verbose, c.dryRun, ruleFiles, inlineRules, defaultFile)
	})
}

func classifyArgs(args []string) (files, rules []string) {
	for _, arg := range args {
		if isRuleSyntax(arg) {
			rules = append(rules, arg)
		} else {
			files = append(files, arg)
		}
	}
	return files, rules
}

func isRuleSyntax(s string) bool {
	return strings.Contains(s, "->") ||
		strings.Contains(s, "<-") ||
		strings.Contains(s, "=") ||
		strings.Contains(s, "#") ||
		strings.HasPrefix(s, "@")
}

func withProfile(path string, fn func() error) error {
	if path == "" {
		return fn()
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		return err
	}
	defer pprof.StopCPUProfile()
	return fn()
}

func runRelo(verbose, dryRun bool, ruleFiles []string, inlineRules []string, defaultFiles bool) error {
	var merged rules.File

	for _, rulesPath := range ruleFiles {
		data, err := os.ReadFile(rulesPath)
		if err != nil {
			if defaultFiles && os.IsNotExist(err) {
				continue
			}
			return err
		}
		f, err := rules.Parse(rulesPath, data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", rulesPath, err)
		}
		merged.Directives = append(merged.Directives, f.Directives...)
		merged.Rules = append(merged.Rules, f.Rules...)
	}

	for i, r := range inlineRules {
		f, err := rules.Parse(fmt.Sprintf("arg[%d]", i), []byte(r))
		if err != nil {
			return fmt.Errorf("parsing rule %q: %w", r, err)
		}
		merged.Directives = append(merged.Directives, f.Directives...)
		merged.Rules = append(merged.Rules, f.Rules...)
	}

	if len(merged.Rules) == 0 {
		return fmt.Errorf("no rules specified")
	}

	// Process directives.
	opts := &relo.Options{}
	var fmtCmd string
	for _, d := range merged.Directives {
		switch d.Key {
		case "stubs":
			opts.Stubs = d.Value == "" || d.Value == "true"
		case "fmt":
			fmtCmd = d.Value
		}
	}

	// Load index.
	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	// Convert rules to relos. Use absolute dir so that target paths
	// are absolute, matching the absolute paths from mast.Load.
	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}
	relos, fileMoves, err := relo.FromRules(ix, merged.Rules, absDir)
	if err != nil {
		return err
	}

	// Compile plan.
	plan, err := relo.Compile(ix, relos, fileMoves, opts)
	if err != nil {
		return fmt.Errorf("compiling plan: %w", err)
	}

	// Print warnings to stderr (always).
	for _, w := range plan.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	// Dry run or verbose: print plan summary.
	if dryRun || verbose {
		w := os.Stdout
		if verbose {
			w = os.Stderr
		}
		printDeclSummary(w, ix, relos, fileMoves, absDir)
		fmt.Fprintln(w)
		for _, edit := range plan.Edits {
			fmt.Fprintf(w, "  %-7s %s\n", editAction(edit), relPath(absDir, edit.Path))
		}
		if dryRun {
			return nil
		}
	}

	// Apply plan.
	if err := relo.Apply(plan); err != nil {
		return fmt.Errorf("applying plan: %w", err)
	}

	// Run formatter if configured.
	if fmtCmd != "" {
		var paths []string
		for _, edit := range plan.Edits {
			if !edit.IsDelete {
				paths = append(paths, edit.Path)
			}
		}
		if len(paths) > 0 {
			if err := runFormatter(fmtCmd, paths); err != nil {
				return fmt.Errorf("running formatter: %w", err)
			}
		}
	}

	return nil
}

// runFormatter splits the command string and runs it with the given file paths appended.
func runFormatter(cmdStr string, paths []string) error {
	parts := strings.Fields(cmdStr)
	if len(parts) == 0 {
		return nil
	}
	args := make([]string, 0, len(parts)-1+len(paths))
	args = append(args, parts[1:]...)
	args = append(args, paths...)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func editAction(edit relo.FileEdit) string {
	switch {
	case edit.IsNew:
		return "create"
	case edit.IsDelete:
		return "delete"
	default:
		return "modify"
	}
}

func printDeclSummary(w *os.File, ix *mast.Index, relos []relo.Relo, fileMoves []relo.FileMove, absDir string) {
	for _, fm := range fileMoves {
		from := relPath(absDir, fm.From)
		to := relPath(absDir, fm.To)
		fmt.Fprintf(w, "  move    %s -> %s\n", from, to)
	}

	for _, r := range relos {
		grp := ix.Group(r.Ident)
		if grp == nil {
			continue
		}

		name := grp.Name
		kind := objectKindString(grp.Kind)

		// Source file.
		var srcFile string
		if def := grp.DefIdent(); def != nil && def.File != nil {
			srcFile = relPath(absDir, def.File.Path)
		}

		// Describe the action.
		switch {
		case r.Detach && r.MoveTo != "" && r.Rename != "":
			fmt.Fprintf(w, "  detach  %-7s %s -> %s=%s\n",
				kind, name, relPath(absDir, r.MoveTo), r.Rename)
		case r.Detach && r.MoveTo != "":
			fmt.Fprintf(w, "  detach  %-7s %s -> %s\n",
				kind, name, relPath(absDir, r.MoveTo))
		case r.Detach:
			fmt.Fprintf(w, "  detach  %-7s %s\n", kind, name)

		case r.MethodOf != "" && r.MoveTo != "":
			fmt.Fprintf(w, "  attach  %-7s %s -> %s#%s in %s\n",
				kind, name, r.MethodOf, r.Rename, relPath(absDir, r.MoveTo))
		case r.MethodOf != "":
			fmt.Fprintf(w, "  attach  %-7s %s -> %s#%s\n",
				kind, name, r.MethodOf, r.Rename)

		case r.MoveTo != "" && r.Rename != "":
			fmt.Fprintf(w, "  move    %-7s %s=%s  %s -> %s\n",
				kind, name, r.Rename, srcFile, relPath(absDir, r.MoveTo))
		case r.MoveTo != "":
			fmt.Fprintf(w, "  move    %-7s %-20s %s -> %s\n",
				kind, name, srcFile, relPath(absDir, r.MoveTo))
		case r.Rename != "":
			fmt.Fprintf(w, "  rename  %-7s %s -> %s  in %s\n",
				kind, name, r.Rename, srcFile)
		}
	}
}
