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

func main() {
	ctx := context.Background()
	ok, err := (clingy.Environment{
		Name: "gorelo",
	}).Run(ctx, func(cmds clingy.Commands) {
		cmds.New("apply", "apply rules from files and/or inline arguments", new(cmdApply))
		cmds.New("check", "dry-run: print plan without writing", &cmdApply{dryRun: true})
		cmds.New("ls", "list declarations in the codebase", new(cmdLs))
		cmds.New("help", "print rule syntax and examples", new(cmdHelp))
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorelo: %v\n", err)
	}
	if !ok || err != nil {
		os.Exit(1)
	}
}

type cmdHelp struct{}

func (c *cmdHelp) Setup(params clingy.Parameters) {}

func (c *cmdHelp) Execute(ctx context.Context) error {
	fmt.Fprint(clingy.Stdout(ctx), helpText)
	return nil
}

const helpText = `gorelo is a tool for refactoring Go codebases.
It applies moves, renames and rewrites to every file that
references the affected declarations. It tries to do the refactoring
across taking into account build tags.

Each positional argument is either a path to a .rules file or an
inline rule string. Arguments containing rule syntax (arrows, =, #)
are treated as inline rules; everything else is loaded as a file.
With no arguments, gorelo.rules is loaded by default.

Examples:
  gorelo apply                                    # apply gorelo.rules
  gorelo apply refactor.rules                     # different rules file
  gorelo apply "Server -> server.go"              # inline rule
  gorelo apply refactor.rules "X=Y -> target.go"  # file plus inline rule
  gorelo apply "@stubs" "Server -> server.go"     # with stubs directive
  gorelo check                                    # preview without writing
  gorelo check refactor.rules                     # preview specific file
  gorelo ls                                       # list all declarations
  gorelo ls --json                                # JSON output for tooling

Rule syntax:
  Server -> server.go                  # move declaration to file (forward)
  server.go <- Server Client           # move declarations to file (reverse)
  server.go <-                         # multiline reverse block
      Server
      Client

  old.go -> new.go                     # move a whole file (preserves order)
  src/greet.go -> dst/greet.go         # file move across packages

  OldName=NewName -> target.go         # move and rename
  Type#Field=NewField                  # rename a struct field
  Type#Outer.Inner=NewInner            # rename nested anonymous struct field

  fn=Type#Method                       # attach function as a method
  fn=Type#Method -> server.go          # attach and move
  Type#Method=!                        # detach method, keep the name
  Type#Method=!newFn                   # detach and rename
  Type#Method=! -> util.go             # detach and move

  file.go:Server -> target.go          # disambiguate by source file
  ./pkg.Name -> target.go              # disambiguate by relative package
  github.com/x/y.Name -> target.go     # disambiguate by full package path

Directives (in rules files):
  # run formatter on modified files
  @fmt goimports -w -local example.com/project
  # generate //go:fix backward-compat stubs
  @stubs=true
`

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
		for _, edit := range plan.Edits {
			w := os.Stdout
			if verbose {
				w = os.Stderr
			}
			fmt.Fprintf(w, "%s %s\n", editAction(edit), edit.Path)
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
