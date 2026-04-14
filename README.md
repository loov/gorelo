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
gorelo <command> [flags]
```

| Command | Description                                            |
|---------|--------------------------------------------------------|
| `apply` | Apply rules from a file and/or `--rule` flags          |
| `check` | Print the plan without writing (dry-run of `apply`)    |
| `do`    | Apply inline rule arguments directly (no file)         |

`apply` and `check` share these flags:

| Flag              | Default        | Description                                      |
|-------------------|----------------|--------------------------------------------------|
| `-f`, `--file`    | `gorelo.rules` | Path to a rules file                             |
| `-r`, `--rule`    | (repeatable)   | Inline rule, same syntax as a rules file line    |
| `-v`, `--verbose` | `false`        | Print each file edit to stderr                   |
| `--stubs`         | `false`        | Generate `//go:fix` inline backward-compat stubs |

`do` takes rule arguments positionally and accepts `-v` / `--stubs`.

```bash
gorelo apply                                       # apply gorelo.rules
gorelo apply -f refactor.rules                     # different rules file
gorelo apply -r "Server -> server.go"              # file plus inline rule
gorelo check -f gorelo.rules                       # preview without writing
gorelo do "Server -> server.go"                    # inline, no file
gorelo do -v "server.go <- Server Client"          # inline, verbose
```

See [EXAMPLE.md](EXAMPLE.md) for a walkthrough of splitting a flat package
into subpackages (`server/`, `db/`, `service/`) with private-to-public renames.

## Rules syntax

### Moving declarations

Forward notation is compact and works well for inline `-r` flags:

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
