package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime/pprof"
	"strings"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

func main() {
	verbose := flag.Bool("v", false, "print each file edit to stderr")
	dryRun := flag.Bool("dry", false, "print plan without applying")
	rulesFile := flag.String("f", "gorelo.rules", "path to a .rules file")
	stubs := flag.Bool("stubs", false, "generate //go:fix inline backward-compatibility stubs")

	var inlineRules []string
	flag.Func("r", "inline rule (repeatable, same syntax as rules file lines)", func(val string) error {
		inlineRules = append(inlineRules, val)
		return nil
	})

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `gorelo — move and rename Go declarations across files and packages.

Gorelo loads all Go packages in the current module (including all build
constraints), applies the requested moves and renames, and rewrites every
file that references the affected declarations.

Usage:
  gorelo [flags]

Flags:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Rules can come from a file (-f, default "gorelo.rules") and/or from
inline -r flags. Both sources are merged.

Examples:
  gorelo                                          # apply gorelo.rules
  gorelo -f refactor.rules                        # use a different rules file
  gorelo -r "Server -> server.go"                 # inline rule
  gorelo -r "server.go <- Server Client" -v       # reverse notation, verbose
  gorelo -dry -f gorelo.rules                     # preview without writing

Rule syntax:
  Server -> server.go                  # move declaration to file (forward)
  server.go <- Server Client           # move declarations to file (reverse)
  server.go <-                         # multiline reverse block
      Server
      Client

  OldName=NewName -> target.go         # move and rename
  Type#Field=NewField                  # rename a struct field
  Type#Outer.Inner=NewInner            # rename nested anonymous struct field

  file.go:Server -> target.go          # disambiguate by source file
  ./pkg.Name -> target.go              # disambiguate by relative package
  github.com/x/y.Name -> target.go    # disambiguate by full package path

Directives (in rules files):
  @fmt goimports                       # run formatter on modified files
  @stubs=true                          # generate //go:fix backward-compat stubs
`)
	}

	cpuprofile := flag.String("cpuprofile", "", "write CPU profile to file")

	flag.Parse()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}

	if err := run(*verbose, *dryRun, *rulesFile, *stubs, inlineRules); err != nil {
		fmt.Fprintf(os.Stderr, "gorelo: %v\n", err)
		os.Exit(1)
	}
}

func run(verbose, dryRun bool, rulesPath string, stubsFlag bool, inlineRules []string) error {
	// Collect rules from file and -r flags.
	var merged rules.File

	data, err := os.ReadFile(rulesPath)
	if err == nil {
		f, err := rules.Parse(rulesPath, data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", rulesPath, err)
		}
		merged.Directives = append(merged.Directives, f.Directives...)
		merged.Rules = append(merged.Rules, f.Rules...)
	} else if !os.IsNotExist(err) {
		return err
	}

	for i, r := range inlineRules {
		f, err := rules.Parse(fmt.Sprintf("-r[%d]", i), []byte(r))
		if err != nil {
			return fmt.Errorf("parsing -r %q: %w", r, err)
		}
		merged.Directives = append(merged.Directives, f.Directives...)
		merged.Rules = append(merged.Rules, f.Rules...)
	}

	if len(merged.Rules) == 0 {
		return fmt.Errorf("no rules specified")
	}

	// Process directives.
	opts := &relo.Options{Stubs: stubsFlag}
	var fmtCmd string
	for _, d := range merged.Directives {
		switch d.Key {
		case "stubs":
			if !stubsFlag {
				opts.Stubs = d.Value == "" || d.Value == "true"
			}
		case "fmt":
			fmtCmd = d.Value
		}
	}

	// Load index.
	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	// Convert rules to relos.
	relos, err := relo.FromRules(ix, merged.Rules, ".")
	if err != nil {
		return err
	}

	// Compile plan.
	plan, err := relo.Compile(ix, relos, opts)
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
