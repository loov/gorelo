// Package rules parses relocation rule files that describe how Go
// declarations should be moved between files and optionally renamed.
//
// # Syntax overview
//
// A rules file is a sequence of rules, directives, comments, and blank lines.
// All file paths and package paths use forward slashes (/) as separators,
// regardless of the host operating system.
//
// # Comments
//
// Lines starting with # are comments. A # preceded by whitespace also starts
// an inline comment. The # inside an item token (e.g. Type#Field) is not
// treated as a comment.
//
//	# This is a comment.
//	server.go <- Server  # This is an inline comment.
//
// # Rules
//
// Each rule maps one or more items to a destination file path. There are
// three notations:
//
// Forward notation places items on the left and the destination on the right:
//
//	Server ServerOption -> server.go
//
// Reverse notation places the destination on the left:
//
//	server.go <- Server ServerOption
//
// Multiline notation uses reverse with indented continuation lines:
//
//	server.go <-
//	    Server
//	    ServerOption
//
// In multiline notation, each indented line may contain one or more
// whitespace-separated items. The block ends at the next blank line,
// directive, or non-indented rule.
//
// # Items
//
// An item identifies a declaration to move. In its simplest form it is
// just a name:
//
//	Server
//
// Items can carry additional modifiers described below. All modifiers
// are concatenated without spaces into a single token.
//
// # Renaming declarations
//
// Use = to rename a declaration at the destination:
//
//	Server=Core ServerOptions=Options -> server/core.go
//
// # Renaming struct fields
//
// Use # to target a field within a struct, and = to rename it:
//
//	server/core.go <-
//	    ServerOptions#Listen=Address
//
// For fields nested inside anonymous (embedded) structs, use a dotted
// path through the anonymous field names:
//
//	server/core.go <-
//	    ServerOptions#Limits.min=Min
//
// A field can also be referenced without renaming:
//
//	server/core.go <-
//	    ServerOptions#Listen
//
// # Source specifiers
//
// By default an item is resolved in the current package. A source
// specifier overrides this.
//
// Use : to specify a source file path (relative or with ./):
//
//	server/core_linux.go <-
//	    server_linux.go:File
//	    ./util/file_linux.go:File
//
// Any path containing / is treated as a package path. The last dot
// after the last / separates the package from the declaration name.
// This works for both relative and absolute package paths:
//
//	server/core_linux.go <-
//	    ./util.File
//	    github.com/loov/gorelo.Server
//
// Source specifiers can be combined with renames and field renames:
//
//	server.go <- file.go:Server=Core
//	server.go <- ./util.Server#Listen=Address
//	server.go <- github.com/loov/gorelo.Server=Core
//
// # Directives
//
// Lines starting with @ declare key-value directives that configure
// processing behavior. They are not rules and do not move or rename
// anything.
//
// Space-separated form:
//
//	@fmt goimports
//
// Equals-separated form:
//
//	@stubs=true
//
// Flag form (no value):
//
//	@verbose
//
// # Item grammar
//
// The full grammar for a single item token is:
//
//	item   = [source] name [rename | field]
//	source = path ":"           # file source
//	       | pkg "."            # package source (any path with "/")
//	name   = identifier
//	rename = "=" identifier
//	field  = "#" fieldpath ["=" identifier]
//	fieldpath = identifier {"." identifier}
package rules
