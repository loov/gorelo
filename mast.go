// Package mast parses Go source code and links all ast.Ident nodes
// that refer to the same logical entity across files and packages.
package mast

import (
	"go/ast"
	"go/token"
)

// Config controls how packages are loaded.
type Config struct {
	Dir string   // working directory
	Env []string // environment variables
}

// Index holds the result of loading and linking identifiers.
type Index struct {
	Fset   *token.FileSet
	Pkgs   []*Package
	Errors []error

	groups     map[*ast.Ident]*Group
	groupsByKey map[objectKey]*Group
}

// Group returns all idents that must be renamed together with id,
// or nil if id is untracked (blank, builtin, universe).
func (ix *Index) Group(id *ast.Ident) *Group {
	return ix.groups[id]
}

// Package represents a parsed and type-checked Go package.
type Package struct {
	Name  string
	Path  string
	Files []*File
}

// File represents a single Go source file.
type File struct {
	Path     string
	Syntax   *ast.File
	BuildTag string // the build constraint, if any
}

// Group represents a set of identifiers that refer to the same entity.
type Group struct {
	Name   string
	Kind   ObjectKind
	Pkg    string // package path where defined
	Idents []*Ident
}

// Ident is a single identifier occurrence within a group.
type Ident struct {
	Ident *ast.Ident
	File  *File
	Kind  IdentKind
}

// IdentKind distinguishes definitions from uses.
type IdentKind int

const (
	Def IdentKind = iota
	Use
)

// ObjectKind classifies the entity an identifier refers to.
type ObjectKind int

const (
	Unknown ObjectKind = iota
	TypeName
	Func
	Method
	Field
	Var
	Const
	PackageName
	Label
)

// Load parses and type-checks packages matching patterns, including
// all files regardless of build constraints. Returns an Index linking
// all identifiers.
func Load(cfg *Config, patterns ...string) (*Index, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	return load(cfg, patterns...)
}
