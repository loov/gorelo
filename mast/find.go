package mast

import (
	"go/ast"
	"strings"
)

// FindDef searches loaded packages for a top-level definition with the given name
// and returns its defining *ast.Ident, suitable for use in relo.Relo.
//
// If source is non-empty, the search is narrowed: source is matched against
// package import paths (exact match) and file paths (suffix match), so it can
// be a full import path like "example.com/pkg" or a relative file name like
// "model.go".
//
// Returns nil if no tracked definition is found.
func (ix *Index) FindDef(name, source string) *ast.Ident {
	for _, pkg := range ix.Pkgs {
		pkgMatch := source == "" || pkg.Path == source
		for _, file := range pkg.Files {
			if !pkgMatch && !strings.HasSuffix(file.Path, source) {
				continue
			}
			for _, decl := range file.Syntax.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Name.Name == name && ix.Group(d.Name) != nil {
						return d.Name
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.Name == name && ix.Group(s.Name) != nil {
								return s.Name
							}
						case *ast.ValueSpec:
							for _, id := range s.Names {
								if id.Name == name && ix.Group(id) != nil {
									return id
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// FindFieldDef searches for a field definition within a struct declaration
// named typeName, then looks for a field named fieldName in that struct's
// field list. The source parameter filters the search (same rules as FindDef).
//
// The struct may be a named type (type T struct{...}) or an anonymous struct
// used as the type or value of a variable declaration (var V struct{...} or
// var V = struct{...}{}).
//
// Only direct (non-embedded) fields are searched; nested field paths
// (e.g. "Limits.Min") are not supported.
//
// Returns nil if the struct or field is not found, or if the field ident
// is not tracked by the index.
func (ix *Index) FindFieldDef(typeName, fieldName, source string) *ast.Ident {
	for _, pkg := range ix.Pkgs {
		pkgMatch := source == "" || pkg.Path == source
		for _, file := range pkg.Files {
			if !pkgMatch && !strings.HasSuffix(file.Path, source) {
				continue
			}
			for _, decl := range file.Syntax.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.Name != typeName {
							continue
						}
						if id := ix.findFieldInStruct(s.Type, fieldName); id != nil {
							return id
						}
					case *ast.ValueSpec:
						if !valueSpecHasName(s, typeName) {
							continue
						}
						// Check explicit type: var V struct{...}
						if id := ix.findFieldInStruct(s.Type, fieldName); id != nil {
							return id
						}
						// Check composite literal: var V = struct{...}{...}
						for _, val := range s.Values {
							cl, ok := val.(*ast.CompositeLit)
							if !ok {
								continue
							}
							if id := ix.findFieldInStruct(cl.Type, fieldName); id != nil {
								return id
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// findFieldInStruct looks for a named field in expr if it is a *ast.StructType.
func (ix *Index) findFieldInStruct(expr ast.Expr, fieldName string) *ast.Ident {
	st, ok := expr.(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	for _, field := range st.Fields.List {
		for _, id := range field.Names {
			if id.Name == fieldName && ix.Group(id) != nil {
				return id
			}
		}
	}
	return nil
}

// valueSpecHasName reports whether the ValueSpec declares a name matching target.
func valueSpecHasName(s *ast.ValueSpec, target string) bool {
	for _, id := range s.Names {
		if id.Name == target {
			return true
		}
	}
	return false
}
