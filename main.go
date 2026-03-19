package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
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

	flag.Parse()

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

	// Dry run: print plan and exit.
	if dryRun {
		for _, edit := range plan.Edits {
			action := "modify"
			if edit.IsNew {
				action = "create"
			} else if edit.IsDelete {
				action = "delete"
			}
			fmt.Printf("%s %s\n", action, edit.Path)
		}
		return nil
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

	// Verbose: print each file edit.
	if verbose {
		for _, edit := range plan.Edits {
			action := "modify"
			if edit.IsNew {
				action = "create"
			} else if edit.IsDelete {
				action = "delete"
			}
			fmt.Fprintf(os.Stderr, "%s %s\n", action, edit.Path)
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
	args := append(parts[1:], paths...)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
