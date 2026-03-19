// Package relo moves and renames Go declarations across files and packages.
//
// The entry point is [Compile], which takes a [mast.Index] (a multi-AST
// index of the loaded module) and a slice of [Relo] instructions, and
// returns a [Plan] containing the file edits. [Apply] writes the plan to
// disk.
//
// # Workflow
//
// A typical caller:
//
//  1. Loads the module with [mast.Load] to obtain a [mast.Index].
//  2. Builds []Relo instructions, either directly or via [FromRules].
//  3. Calls [Compile] to produce a [Plan].
//  4. Inspects [Plan.Warnings] and [Plan.Edits].
//  5. Calls [Apply] to write changes to disk.
//
// # Compilation phases
//
// Compile runs the following phases in order:
//
//   - Phases 0–1 (resolve): validate each Relo, deduplicate by mast group,
//     and synthesize related moves. For example, when a type is moved
//     cross-package its methods are automatically added. Unexported methods
//     with no external callers stay unexported; those called from outside
//     the type's own methods are auto-exported.
//
//   - Phases 2–3 (spans): compute the byte ranges of declarations and
//     specs to extract, respecting const/var/import block semantics.
//     Partially moving an iota-dependent const block is rejected.
//
//   - Phase 4 (constraints): warn about mixed build constraints when
//     declarations from different constraint groups target the same file.
//
//   - Phase 5 (conflicts): detect movement conflicts (same group sent to
//     different targets), naming conflicts (duplicate names in the target
//     file), circular imports, references to unexported or main-package
//     symbols, and build-constraint propagation issues.
//
//   - Phase 6 (renames): walk mast groups to find every occurrence of each
//     renamed identifier and produce text edits. When stubs are enabled,
//     old names in the source file are preserved for backward compatibility.
//
//   - Phase 7 (imports): determine which imports the moved declarations
//     need at the target, resolve name collisions with aliases, and remove
//     self-imports.
//
//   - Phase 7b (consumers): rewrite files that import moved symbols from
//     external packages — updating qualifier expressions and imports.
//
//   - Phase 8 (assemble): extract declarations from source files, build
//     target files, apply renames and import edits, remove moved
//     declarations from sources, and optionally generate //go:fix
//     backward-compatibility stubs.
//
// # Stubs
//
// When [Options.Stubs] is true, Compile generates //go:fix type aliases
// and wrapper functions in the original package so that existing consumers
// continue to compile during an incremental migration.
package relo
