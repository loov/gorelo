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
