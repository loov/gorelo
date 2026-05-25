# gorelo

A CLI for refactoring Go codebases. Moves declarations between files
and packages, renames declarations and struct fields, attaches and
detaches methods, and updates every reference in the module across all
build constraints.

## When to use gorelo

- Move a type, function, var, or const to another file or package.
- Rename a declaration and update every caller.
- Convert a function to a method, or detach a method back to a function.
- Rename a struct field (including nested anonymous-struct fields).
- Split a flat package into subpackages, optionally exporting private
  identifiers during the split.

## Workflow

1. Explore: `gorelo ls`, `gorelo refs <name>`, `gorelo deps <name>`,
   `gorelo coverage --for <type> <entries>`.
2. Preview: `gorelo check <rules>`.
3. Apply: `gorelo apply <rules>` (`-v` to log every edit).

## Commands

- `apply [rule-or-file ...]` — apply rules; default file `gorelo.rules`.
- `check [rule-or-file ...]` — dry-run.
- `ls [specifier ...]` — list package-level declarations.
  Flags: `--json`, `--refs`, `--deps`, `--detail` (= `--refs --deps`).
- `refs <specifier>` — references to a declaration. Flag: `--json`.
- `deps <specifier>` — what a declaration depends on. Flag: `--json`.
- `coverage --for <type> <entry ...>` — see "Coverage" below.
  Flags: `--json`, `--by-method`.
- `help` — rule-syntax cheat sheet.
- `skill` — this guide.

Each positional argument to `apply`/`check` is either a `.rules` file
path or an inline rule string. An argument is treated as inline when it
contains `->`, `<-`, `=`, `#`, or starts with `@`.

## Identifier specifiers

- `Name` — bare; errors if ambiguous across packages.
- `./pkg.Name` — qualified by relative package directory.
- `github.com/x/y.Name` — qualified by full import path.
- `file.go:Name` — qualified by source file (suffix match).
- `Type#Method` — a method or field on a type.
- `Type#Outer.Inner` — a nested anonymous struct field.

Globs (`*`, `?`) are accepted only in `coverage` entry specifiers and
in the method part of `--for Type#Glob*`. Not accepted elsewhere.

## Rule syntax

Move declarations:

```
Server ServerOption -> server.go     # forward
server.go <- Server ServerOption     # reverse
server.go <-                         # reverse, multi-line
    Server
    ServerOption
    Config
```

Rename:

```
OldName=NewName                      # rename in place
OldName=NewName -> target.go         # rename and move
Type#Field=NewField                  # rename a struct field
Type#Outer.Inner=NewInner            # rename a nested anonymous field
```

Attach (function → method) and detach (method → function):

```
fn=Type#Method                       # attach fn as (*Type).Method
fn=Type#Method -> server.go          # attach and move
Type#Method=!                        # detach, keep the name
Type#Method=!newFn                   # detach and rename
Type#Method=! -> util.go             # detach and move
```

Move a whole file (preserves declaration order and doc comments; the
destination must not exist):

```
old.go -> new.go                     # within the same package
src/greet.go -> dst/greet.go         # across packages
```

Directives (in `.rules` files; also accepted as bare inline args):

```
@fmt goimports -w -local example.com/proj   # formatter for changed files
@stubs=true                                  # //go:fix backward-compat aliases
```

## Coverage

`coverage` answers "which methods of type T does each of these callers
actually reach, transitively?" It's a scoped static call-graph slice.

Use it to:

- See which tests exercise which methods of an API
  (`gorelo coverage --for ./db.DB 'Test*'`).
- Find unused methods on a type (methods with zero reaching entries) —
  candidates for deletion before a refactor.
- Plan a type split: group methods by which entries call them
  (`--by-method` inverts the report).
- Verify a refactor: re-run before and after to confirm coverage hasn't
  regressed.

The walk follows direct calls through function and method bodies in
the loaded module.

## JSON output

`ls`, `refs`, `deps`, and `coverage` accept `--json`. Schemas:

- `ls`: `[{file, package, build_tag?, decls: [{name, kind, receiver?,
  line, end, lines, refs?, deps?}]}]`
- `refs`/`deps`: `[{name, kind, def_file, def_line, refs|deps: [...]}]`
- `coverage`: `{type, package, filter?, methods: [...], entries: [...]}`

## Pitfalls

- **Methods follow their type.** Moving a type takes its methods with
  it. List a method explicitly only when you want to rename or relocate
  it differently from the type.
- **Cross-package moves reject unexported names with external refs.**
  Add a rename to export first: `oldName=NewName -> dst/file.go`.
- **File-move target must not exist.** To merge into an existing file,
  use per-declaration rules.
- **Build tags are unified.** The same name in `_linux.go` and
  `_windows.go` is one rename group; moves apply to every platform.
- **Default rule file.** `gorelo apply` with no args reads
  `gorelo.rules` from the working directory. Pass the file explicitly
  when scripting.

## Minimal example

```
# move Server to its own package, exporting the constructor
server/server.go <-
    Server
    newServer=New

# detach and rename a method
Server#cleanup=! -> shutdown.go
```

```
gorelo check gorelo.rules
gorelo apply gorelo.rules
```
