# mast

Package mast parses Go source code and links all `ast.Ident` nodes that refer to the same logical entity across files and packages. It loads all files regardless of build constraints, type-checks each build-tag partition separately, and merges the results into a unified index.

## Usage

```go
ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")

// Find which group an identifier belongs to.
grp := ix.Group(ident) // returns *mast.Group or nil

// A Group contains all identifiers that refer to the same entity.
for _, id := range grp.Idents {
    fmt.Printf("%s at %s (%v)\n", id.Ident.Name, ix.Fset.Position(id.Ident.Pos()), id.Kind)
}
```

`Load` returns an `Index` containing:
- `Fset` -- the shared `token.FileSet`
- `Pkgs` -- all loaded packages with their files and parsed ASTs
- `Errors` -- non-fatal errors encountered during loading

Each `Group` has:
- `Name` -- the identifier name
- `Kind` -- classification (`TypeName`, `Func`, `Method`, `Field`, `Var`, `Const`, `PackageName`, `Label`)
- `Pkg` -- the package path where defined
- `Idents` -- all occurrences, each marked as `Def` or `Use`

Identifiers that are untracked (blank `_`, builtins, universe-scope) return `nil` from `Group()`.

## mast-browser

`cmd/mast-browser` is a web UI for browsing loaded packages. Click any identifier to see all locations in its rename group across files.

```
cd yourproject && go run github.com/loov/mast/cmd/mast-browser -dir . -listen localhost:8080
```

## Testing

```
go test ./...
```

The test suite uses `testdata/example/`, a multi-file Go package covering: named types, generics, type aliases, interfaces, struct embedding (single/multi-level, cross-package, interface-in-struct), methods (value/pointer receiver, expressions), channels, closures, labels, local variable scoping, shadowing, named returns, blank identifiers, init functions, import aliases (renamed, dot, side-effect), build constraints (compound, negated, custom, ignore), and cross-package references.
