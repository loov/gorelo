package main

import (
	"context"
	"fmt"
	"os"

	"github.com/zeebo/clingy"
)

func main() { os.Exit(run()) }

func run() int {
	ctx := context.Background()
	ok, err := (clingy.Environment{
		Name: "gorelo",
	}).Run(ctx, func(cmds clingy.Commands) {
		cmds.New("apply", "apply rules from files and/or inline arguments", new(cmdApply))
		cmds.New("check", "dry-run: print plan without writing", &cmdApply{dryRun: true})
		cmds.New("deps", "show what a declaration depends on", new(cmdDeps))
		cmds.New("ls", "list declarations in the codebase", new(cmdLs))
		cmds.New("refs", "show where declarations are referenced", new(cmdRefs))
		cmds.New("help", "print rule syntax and examples", new(cmdHelp))
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gorelo: %v\n", err)
	}
	if !ok || err != nil {
		return 1
	}
	return 0
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
  gorelo ls Server                                # list a type and its methods
  gorelo ls ./pkg.Server                          # qualified by package
  gorelo ls server.go:Server                      # qualified by file
  gorelo deps Server                              # show what Server depends on
  gorelo deps ./pkg.Server                        # qualified by package
  gorelo refs Server                              # show references to Server
  gorelo refs ./pkg.Server                        # qualified by package
  gorelo refs Server#Start                        # references to a method

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
