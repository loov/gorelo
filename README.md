# gorelo

Move and rename Go declarations across files and packages.

Gorelo loads all Go packages in the current module (respecting all build
constraints simultaneously), applies the requested moves and renames, and
rewrites every file that references the affected declarations.

## Install

```
go install github.com/loov/gorelo@latest
```

## Usage

```
gorelo <command> [flags] [rule-or-file ...]
```

| Command | Description                                            |
|---------|--------------------------------------------------------|
| `apply` | Apply rules from files and/or inline arguments         |
| `check` | Print the plan without writing (dry-run of `apply`)    |

Each positional argument is either a path to a `.rules` file or an
inline rule string. Arguments containing rule syntax (`->`, `<-`, `=`,
`#`) or starting with `@` are treated as inline rules; everything else
is loaded as a file. With no arguments, `gorelo.rules` is loaded by
default.

`apply` and `check` share these flags:

| Flag              | Default | Description                    |
|-------------------|---------|--------------------------------|
| `-v`, `--verbose` | `false` | Print each file edit to stderr |

```bash
gorelo apply                                       # apply gorelo.rules
gorelo apply refactor.rules                        # different rules file
gorelo apply "Server -> server.go"                 # inline rule
gorelo apply refactor.rules "X=Y -> target.go"     # file plus inline rule
gorelo apply "@stubs" "Server -> server.go"        # with stubs directive
gorelo check                                       # preview without writing
gorelo check refactor.rules                        # preview specific file
```

See [EXAMPLE.md](EXAMPLE.md) for a walkthrough of splitting a flat package
into subpackages (`server/`, `db/`, `service/`) with private-to-public renames.

## Rules syntax

### Moving declarations

Forward notation is compact and works well for inline rules:

```
Server ServerOption -> server.go
```

Reverse notation reads better in rules files since grouping by
destination is the common case:

```
server.go <- Server ServerOption
```

Multiline reverse block (indented continuation):

```
server.go <-
    Server
    ServerOption
    Config
```

### Renaming

Rename a declaration at the destination with `=`:

```
OldName=NewName -> target.go
```

Rename a struct field with `#`:

```
ServerOptions#Listen=Address
```

Rename a nested anonymous struct field with a dotted path:

```
ServerOptions#Limits.Min=MinValue
```

### Attaching and detaching methods

Turn a standalone function into a method by writing the target
method on the right of the rename:

```
start=Server#Start                # attach 'start' as (*Server).Start
StartServer=Server#Start          # attach 'StartServer' as (*Server).Start
start=Server#Start -> server.go   # attach and move in one rule
```

Turn a method into a standalone function with `=!` ("cut"). Leave
the name after `!` empty to keep the method's name:

```
Server#Start=!                    # detach, keep the name "Start"
Server#Start=!startServer         # detach and rename to startServer
Server#Start=! -> util.go         # detach and move
```

### Moving whole files

Point the arrow from a `.go` source path to a `.go` destination path to
move an entire file. The source file's content is transferred verbatim —
declaration order, doc comments, and file-level layout are preserved:

```
old.go -> new.go                       # file rename within the same package
src/greet.go -> dst/greet.go           # move into a different package
```

The destination file must not already exist; per-declaration rules are the
way to merge into an existing file. Cross-package moves reject unexported
declarations with external references — add an explicit rename rule on
another line to export them first.

### Source specifiers

Disambiguate by source file:

```
server_linux.go:Server -> server/core_linux.go
```

By relative or full package path (last `.` after last `/` separates
package from name):

```
./util.Helper -> helpers.go
github.com/loov/gorelo.Server -> server.go
```

### Directives

Directives start with `@` and configure processing behavior:

```
@fmt goimports          # run formatter on modified files
@stubs=true             # generate //go:fix backward-compat stubs
```

## How it works

Gorelo uses a multi-AST approach: it discovers all `.go` files (including
test files and platform-specific variants like `_linux.go`, `_windows.go`)
and type-checks each build-constraint group. This lets it track identifiers
across all platforms in a single run.

The compilation pipeline:

1. Parse rules into relocation instructions
2. Load and type-check all packages (`mast.Load`)
3. Resolve declarations and synthesize related moves (e.g. methods follow their type)
4. Compute source/target spans respecting block semantics (`const`, `var`, `import` groups)
5. Check build-constraint compatibility and detect conflicts
6. Compute rename and import edits
7. Rewrite consumer files that reference moved/renamed declarations
8. Assemble and apply file edits

## mast-browser

A companion web UI for browsing Go packages with identifier
rename-group highlighting.

```
go install github.com/loov/gorelo/cmd/mast-browser@latest
mast-browser -dir . -listen 127.0.0.1:8080
```

Click any identifier to highlight all references in the same rename group.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
