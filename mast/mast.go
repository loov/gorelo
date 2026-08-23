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
		if key.Name != name || key.PkgPath != pkg || grp.Kind != ObjectField {
			continue
		}
		// Embedded field groups have no Def ident — the definition
		// is redirected to the type name's group in resolveInfo.
		hasDef := false
		for _, id := range grp.Idents {
			if id.Kind == IdentDef {
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
	Name      string
	Path      string
	GoVersion string // go directive of the containing module ("1.27"), or "" if unknown
	Files     []*File
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

// DefIdent returns the first Def ident in the group, or nil if none exists.
func (g *Group) DefIdent() *Ident {
	for _, id := range g.Idents {
		if id.Kind == IdentDef {
			return id
		}
	}
	return nil
}

// FindIdent returns the ident in the group that matches target by pointer
// identity and has the given kind, or nil if no match exists.
func (g *Group) FindIdent(target *ast.Ident, kind IdentKind) *Ident {
	for _, id := range g.Idents {
		if id.Ident == target && id.Kind == kind {
			return id
		}
	}
	return nil
}

// IsPackageScope reports whether the group represents a package-scope
// declaration (as opposed to a local variable, parameter, or result).
// A package-scope group has at least one Def ident at file top-level —
// not nested inside any FuncDecl or FuncLit.
func (g *Group) IsPackageScope() bool {
	for _, id := range g.Idents {
		if id.Kind != IdentDef || id.File == nil {
			continue
		}
		if !isInsideFuncBody(id.Ident, id.File.Syntax) {
			return true
		}
	}
	return false
}

// isInsideFuncBody reports whether ident is nested inside any FuncDecl
// (other than being that FuncDecl's own Name) or any FuncLit. The Name
// ident of a top-level FuncDecl is considered package-scope even though
// it is syntactically inside the FuncDecl node.
func isInsideFuncBody(ident *ast.Ident, file *ast.File) bool {
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if ident == fn.Name {
				return true
			}
			if ident.Pos() >= fn.Pos() && ident.End() <= fn.End() {
				found = true
				return false
			}
		case *ast.FuncLit:
			if ident.Pos() >= fn.Pos() && ident.End() <= fn.End() {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// HasUses reports whether the group has any Use idents.
func (g *Group) HasUses() bool {
	for _, id := range g.Idents {
		if id.Kind == IdentUse {
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
	IdentInvalid IdentKind = iota
	IdentDef
	IdentUse
)

func (k IdentKind) String() string {
	switch k {
	case IdentDef:
		return "def"
	case IdentUse:
		return "use"
	}
	return "invalid"
}

// ObjectKind classifies the entity an identifier refers to.
type ObjectKind int

const (
	ObjectUnknown ObjectKind = iota
	ObjectTypeName
	ObjectFunc
	ObjectMethod
	ObjectField
	ObjectVar
	ObjectConst
	ObjectPackageName
	ObjectLabel
)

func (k ObjectKind) String() string {
	switch k {
	case ObjectTypeName:
		return "type"
	case ObjectFunc:
		return "func"
	case ObjectMethod:
		return "method"
	case ObjectField:
		return "field"
	case ObjectVar:
		return "var"
	case ObjectConst:
		return "const"
	case ObjectPackageName:
		return "package"
	case ObjectLabel:
		return "label"
	}
	return "unknown"
}

// TravelsWithType reports whether this kind represents an entity that
// moves with its parent type (methods and fields). These are accessed
// through instances (e.g. svc.Start(), cfg.Host), not as bare package-
// qualified identifiers, so they must not be package-qualified during
// cross-package moves.
func (k ObjectKind) TravelsWithType() bool {
	return k == ObjectMethod || k == ObjectField
}

// HasStub reports whether this kind gets its own backward-compatibility
// stub when moved cross-package with stubs enabled. Methods are excluded
// because they follow the receiver type's alias automatically.
func (k ObjectKind) HasStub() bool {
	switch k {
	case ObjectTypeName, ObjectFunc, ObjectConst, ObjectVar:
		return true
	default:
		return false
	}
}

// Load parses and type-checks packages matching patterns, including
// all files regardless of build constraints. Returns an Index linking
// all identifiers.
func Load(cfg *Config, patterns ...string) (*Index, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	return load(cfg, patterns...)
}
