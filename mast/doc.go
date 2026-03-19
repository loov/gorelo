// Package mast ("multi-AST") loads Go source code across multiple
// packages and build constraints, type-checks every file, and links
// all [go/ast.Ident] nodes that refer to the same logical entity into
// rename groups.
//
// # Overview
//
// Standard Go tooling type-checks one build-tag configuration at a
// time, so platform-specific files are invisible to each other. mast
// solves this by discovering every .go file in a package directory,
// partitioning them by build constraint, type-checking each partition
// separately, and merging the results into a single [Index]. The
// result is a unified view of every identifier across all platforms.
//
// # Loading
//
// Call [Load] with one or more package patterns:
//
//	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
//
// [Load] returns an [Index] containing:
//   - Fset — the shared [go/token.FileSet] for position information.
//   - Pkgs — all loaded packages with their files and parsed ASTs.
//   - Errors — non-fatal errors encountered during loading.
//
// # Identifier groups
//
// Every [go/ast.Ident] that refers to a user-defined entity is
// assigned to a [Group]. A group collects all identifiers — both
// definitions and uses — that refer to the same logical entity across
// files and build-tag partitions. Renaming a group means renaming
// every identifier in it.
//
//	grp := ix.Group(ident) // returns *Group or nil
//	for _, id := range grp.Idents {
//	    fmt.Printf("%s at %s (%v)\n",
//	        id.Ident.Name,
//	        ix.Fset.Position(id.Ident.Pos()),
//	        id.Kind)
//	}
//
// Each [Group] carries:
//   - Name — the identifier name.
//   - Kind — the entity classification ([TypeName], [Func], [Method],
//     [Field], [Var], [Const], [PackageName], [Label]).
//   - Pkg — the package path where the entity is defined.
//   - Idents — every occurrence, each marked as [Def] or [Use].
//
// Identifiers that are blank (_), builtins, or universe-scope return
// nil from [Index.Group].
//
// # Build constraints and test files
//
// mast discovers all .go files in a package directory, including
// _test.go files, and extracts build constraints from //go:build
// directives and filename conventions (_linux.go, _amd64.go, etc.).
//
// Files are partitioned into sets that can be type-checked together.
// When platform-specific files define conflicting top-level names
// (e.g. the same function in both _linux.go and _windows.go), each
// constraint group is type-checked in a separate pass with the
// unconstrained files included in every pass. When there are no
// conflicts all files are type-checked in a single pass.
//
// Identifiers that appear in multiple passes (for example a type
// defined in a shared file and used in two platform files) are
// automatically merged into the same group.
//
// Same-package test files (package foo in _test.go files) are
// type-checked together with the main package files. External test
// files (package foo_test) are type-checked in a separate pass after
// the main package, so they can import it, and are returned as a
// separate [Package] with path "pkg_test".
//
// Every [File] carries a Pkg pointer back to the [Package] it belongs
// to, so from any [Ident] the containing package is available via
// ident.File.Pkg. This is needed when moving declarations between
// packages, where each use site may need its package qualifier and
// imports adjusted.
//
// # Embedded fields
//
// For embedded (anonymous) struct fields the field identifier is
// linked to the embedded type's group, so that renaming the type also
// renames the embedding site.
//
// # Limitations
//
// Type-checker errors are intentionally suppressed. This allows loading
// cross-platform code that would fail to compile under the host's build
// tags (e.g. a syscall used only on Linux), but it also means that
// invalid code is silently accepted. Only AST definitions, uses, and
// selections from the type-checker are retained; diagnostics are
// discarded.
//
// Build-constraint partitioning uses syntactic conflict detection:
// constrained files are type-checked together when they do not define
// the same top-level function, type, variable, or constant names.
// When they do conflict (e.g. the same function in _linux.go and
// _windows.go), each constraint group is type-checked in a separate
// pass with the unconstrained files included in every pass. This is
// based on name equality only — indirect conflicts (A conflicts with B,
// B conflicts with C, but not A with C) and method-level conflicts are
// not detected and may lead to type-check failures that are silently
// swallowed.
//
// Cross-partition linking relies on [go/ast.File.Unresolved]: after all
// type-check passes, identifiers that remain untracked are matched to
// package-scope groups by name. This only links package-level symbols
// (not local variables, fields, or methods). If two package-scope
// symbols share a name (which cannot happen in valid Go but can in
// constraint-partitioned code with suppressed errors), the linking is
// ambiguous.
//
// Fields in anonymous (unnamed) struct types use position-based keys,
// so they form isolated groups that cannot be merged across build
// constraint partitions. Named struct fields are keyed by the owning
// type name, found by scanning the package scope; if the owner cannot
// be determined (e.g. fields from generic type instantiations), the
// field falls back to a position-based key. Position-based keys are
// stable within a single load but are not meaningful across separate
// invocations.
//
// Import aliases are treated as file-scoped (each gets a position-based
// key) to prevent merging two files' aliases for different packages
// into the same group. This means renaming one import alias does not
// automatically rename another file's alias for the same package.
//
// Embedded field identifiers are linked to the embedded type's group
// rather than a dedicated field group. [Index.EmbeddedFieldGroups]
// returns synthetic Use-only groups for composite literal keys and
// selectors that refer to the embedded field by name, but these groups
// have no Def ident — the definition site is part of the type name's
// group.
package mast
