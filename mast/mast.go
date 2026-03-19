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
	Fset        *token.FileSet
	Pkgs        []*Package
	Errors      []error
	FilesByPath map[string]*File // populated after loading

	groups      map[*ast.Ident]*Group
	groupsByKey map[objectKey]*Group
}

// Group returns all idents that must be renamed together with id,
// or nil if id is untracked (blank, builtin, universe).
func (ix *Index) Group(id *ast.Ident) *Group {
	return ix.groups[id]
}

// EmbeddedFieldGroups returns Field groups with the given name and package
// that represent embedded (anonymous) fields.  These groups contain only
// Use idents (composite literal keys and selectors) because the embedded
// field definition ident is linked to the type name's group instead.
func (ix *Index) EmbeddedFieldGroups(name, pkg string) []*Group {
	var groups []*Group
	for key, grp := range ix.groupsByKey {
		if key.Name != name || key.PkgPath != pkg || grp.Kind != Field {
			continue
		}
		// Embedded field groups have no Def ident — the definition
		// is redirected to the type name's group in resolveInfo.
		hasDef := false
		for _, id := range grp.Idents {
			if id.Kind == Def {
				hasDef = true
				break
			}
		}
		if !hasDef {
			groups = append(groups, grp)
		}
	}
	return groups
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
	Pkg      *Package // the package this file belongs to
	Syntax   *ast.File
	Src      []byte // original source bytes
	BuildTag string // the build constraint, if any
}

// Group represents a set of identifiers that refer to the same entity.
type Group struct {
	Name   string
	Kind   ObjectKind
	Pkg    string // package path where defined
	Idents []*Ident
}

// IsPackageScope reports whether the group represents a package-scope
// declaration (as opposed to a local variable, parameter, or result).
// A package-scope group has at least one Def ident at file top-level
// (not inside any FuncDecl).
func (grp *Group) IsPackageScope() bool {
	for _, id := range grp.Idents {
		if id.Kind != Def || id.File == nil {
			continue
		}
		inside := false
		for _, decl := range id.File.Syntax.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if id.Ident.Pos() >= fd.Pos() && id.Ident.End() <= fd.End() {
				// Inside this FuncDecl — package-scope only if
				// it IS the function's name ident itself.
				if id.Ident != fd.Name {
					inside = true
				}
				break
			}
		}
		if !inside {
			return true
		}
	}
	return false
}

// Ident is a single identifier occurrence within a group.
type Ident struct {
	Ident     *ast.Ident
	Qualifier *ast.Ident // package qualifier ident in pkg.Name expressions, or nil
	File      *File
	Kind      IdentKind
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
