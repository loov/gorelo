package mast

import (
	"go/ast"
	"path/filepath"
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
			if !pkgMatch && !fileMatchesSource(file.Path, source) {
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

// FindFieldDef searches for a field or method definition within a type
// declaration named typeName. For struct types it looks for a field named
// fieldPath in the struct's field list; for interface types it looks for a
// method with that name in the interface's method list.
// The source parameter filters the search (same rules as FindDef).
//
// The type may be a named type (type T struct{...} or type T interface{...})
// or an anonymous struct used as the type or value of a variable declaration
// (var V struct{...} or var V = struct{...}{}).
//
// fieldPath may be a dotted path to reach fields in nested anonymous structs.
// For example, "TLS.CertFile" first finds the field TLS, then looks for
// CertFile in TLS's anonymous struct type. Interface methods must be simple
// (non-dotted) names.
//
// If the field or method is not found inline, it falls back to searching for
// a receiver-based method (FuncDecl with Recv) on the type.
//
// Returns nil if the type, field, or method is not found, or if the ident
// is not tracked by the index.
func (ix *Index) FindFieldDef(typeName, fieldPath, source string) *ast.Ident {
	for _, pkg := range ix.Pkgs {
		pkgMatch := source == "" || pkg.Path == source
		for _, file := range pkg.Files {
			if !pkgMatch && !fileMatchesSource(file.Path, source) {
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
						if id := ix.findFieldByPath(s.Type, fieldPath); id != nil {
							return id
						}
					case *ast.ValueSpec:
						if !valueSpecHasName(s, typeName) {
							continue
						}
						// Check explicit type: var V struct{...}
						if id := ix.findFieldByPath(s.Type, fieldPath); id != nil {
							return id
						}
						// Check composite literal: var V = struct{...}{...}
						for _, val := range s.Values {
							cl, ok := val.(*ast.CompositeLit)
							if !ok {
								continue
							}
							if id := ix.findFieldByPath(cl.Type, fieldPath); id != nil {
								return id
							}
						}
					}
				}
			}
		}
	}

	// If not found as a struct field, search for a method on the type.
	for _, pkg := range ix.Pkgs {
		pkgMatch := source == "" || pkg.Path == source
		for _, file := range pkg.Files {
			if !pkgMatch && !fileMatchesSource(file.Path, source) {
				continue
			}
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil {
					continue
				}
				recvType := ReceiverTypeName(fd.Recv)
				if recvType != typeName {
					continue
				}
				// fieldPath for methods is just the method name (no dots).
				if fd.Name.Name == fieldPath && ix.Group(fd.Name) != nil {
					return fd.Name
				}
			}
		}
	}

	return nil
}

// ReceiverTypeName extracts the type name from a method receiver field list.
func ReceiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if ident, ok := t.(*ast.Ident); ok {
		return ident.Name
	}
	if idx, ok := t.(*ast.IndexExpr); ok {
		if ident, ok := idx.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	if idx, ok := t.(*ast.IndexListExpr); ok {
		if ident, ok := idx.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

// findFieldByPath resolves a possibly dotted field path (e.g. "TLS.CertFile")
// within a struct or interface type expression. For a simple name it returns
// the field's (or method's) defining ident; for a dotted path it walks through
// intermediate fields, following anonymous struct types at each step.
// Interface methods are matched only as leaf names (no dotted paths).
func (ix *Index) findFieldByPath(expr ast.Expr, fieldPath string) *ast.Ident {
	parts := strings.Split(fieldPath, ".")

	current := expr
	for i, part := range parts {
		switch t := current.(type) {
		case *ast.StructType:
			if t.Fields == nil {
				return nil
			}
			found := false
			for _, field := range t.Fields.List {
				for _, id := range field.Names {
					if id.Name != part {
						continue
					}
					if i == len(parts)-1 {
						// Last segment: this is the target field.
						if ix.Group(id) != nil {
							return id
						}
						return nil
					}
					// Intermediate segment: descend into the field's type.
					current = field.Type
					found = true
					break
				}
				if found {
					break
				}
			}
			if !found {
				return nil
			}
		case *ast.InterfaceType:
			if t.Methods == nil {
				return nil
			}
			// Interface methods are only leaf names (no dotted paths).
			if i != len(parts)-1 {
				return nil
			}
			for _, method := range t.Methods.List {
				for _, id := range method.Names {
					if id.Name == part && ix.Group(id) != nil {
						return id
					}
				}
			}
			return nil
		default:
			return nil
		}
	}
	return nil // unreachable: strings.Split always returns ≥1 element
}

// fileMatchesSource reports whether the file path ends with the given source
// fragment. It normalises path separators so that a forward-slash source like
// "sub/file.go" matches a Windows path like "C:\proj\sub\file.go".
func fileMatchesSource(filePath, source string) bool {
	return strings.HasSuffix(filePath, filepath.FromSlash(source))
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
