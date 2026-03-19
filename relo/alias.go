package relo

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"strings"

	"github.com/loov/gorelo/mast"
)

// generateAliases generates //go:fix inline stubs for cross-package moves.
func generateAliases(rrs []*resolvedRelo, targetPkgName string, fset *token.FileSet) []string {
	var stubs []string

	for _, rr := range rrs {
		switch rr.Group.Kind {
		case mast.TypeName:
			stubs = append(stubs, generateTypeAlias(rr, targetPkgName, fset))
		case mast.Func:
			stubs = append(stubs, generateFuncAlias(rr, targetPkgName, fset))
		case mast.Const:
			stubs = append(stubs, fmt.Sprintf("//go:fix inline\nconst %s = %s.%s",
				rr.Group.Name, targetPkgName, rr.TargetName))
		case mast.Var:
			stubs = append(stubs, fmt.Sprintf("// Deprecated: Use %s.%s instead.\n//go:fix inline\nvar %s = %s.%s",
				targetPkgName, rr.TargetName, rr.Group.Name, targetPkgName, rr.TargetName))
		case mast.Method:
			// Methods follow receiver type alias; no separate stub.
		}
	}

	return stubs
}

func generateTypeAlias(rr *resolvedRelo, targetPkgName string, fset *token.FileSet) string {
	// Check for type parameters.
	decl := findEnclosingDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if gd, ok := decl.(*ast.GenDecl); ok {
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name != rr.DefIdent.Ident {
				continue
			}
			if ts.TypeParams != nil && len(ts.TypeParams.List) > 0 {
				paramsDef := formatTypeParams(ts.TypeParams, fset)
				var names []string
				for _, field := range ts.TypeParams.List {
					for _, name := range field.Names {
						names = append(names, name.Name)
					}
				}
				paramsUse := strings.Join(names, ", ")
				return fmt.Sprintf("//go:fix inline\ntype %s[%s] = %s.%s[%s]",
					rr.Group.Name, paramsDef, targetPkgName, rr.TargetName, paramsUse)
			}
		}
	}
	return fmt.Sprintf("//go:fix inline\ntype %s = %s.%s",
		rr.Group.Name, targetPkgName, rr.TargetName)
}

func generateFuncAlias(rr *resolvedRelo, targetPkgName string, fset *token.FileSet) string {
	decl := findEnclosingDecl(rr.File.Syntax, rr.DefIdent.Ident)
	fd, ok := decl.(*ast.FuncDecl)
	if !ok {
		return fmt.Sprintf("// TODO: alias for %s", rr.Group.Name)
	}

	var buf bytes.Buffer
	buf.WriteString("//go:fix inline\nfunc ")
	buf.WriteString(rr.Group.Name)
	buf.WriteString("(")
	buf.WriteString(formatFieldList(fd.Type.Params, fset))
	buf.WriteString(")")

	hasResults := fd.Type.Results != nil && len(fd.Type.Results.List) > 0
	if hasResults {
		results := formatResultList(fd.Type.Results, fset)
		if len(fd.Type.Results.List) > 1 || (len(fd.Type.Results.List) == 1 && len(fd.Type.Results.List[0].Names) > 0) {
			buf.WriteString(" (")
			buf.WriteString(results)
			buf.WriteString(")")
		} else {
			buf.WriteString(" ")
			buf.WriteString(results)
		}
	}

	buf.WriteString(" { ")
	if hasResults {
		buf.WriteString("return ")
	}
	buf.WriteString(targetPkgName)
	buf.WriteString(".")
	buf.WriteString(rr.TargetName)
	buf.WriteString("(")
	buf.WriteString(paramNames(fd.Type.Params))
	buf.WriteString(") }")

	return buf.String()
}

func formatTypeParams(fl *ast.FieldList, fset *token.FileSet) string {
	var parts []string
	for _, field := range fl.List {
		typeStr := nodeString(field.Type, fset)
		names := make([]string, len(field.Names))
		for i, n := range field.Names {
			names[i] = n.Name
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typeStr)
	}
	return strings.Join(parts, ", ")
}

func formatFieldList(fl *ast.FieldList, fset *token.FileSet) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	paramIdx := 0
	for _, field := range fl.List {
		typeStr := nodeString(field.Type, fset)
		if len(field.Names) == 0 {
			name := fmt.Sprintf("p%d", paramIdx)
			paramIdx++
			parts = append(parts, name+" "+typeStr)
		} else {
			names := make([]string, len(field.Names))
			for i, n := range field.Names {
				if n.Name == "_" {
					names[i] = fmt.Sprintf("p%d", paramIdx)
				} else {
					names[i] = n.Name
				}
				paramIdx++
			}
			parts = append(parts, strings.Join(names, ", ")+" "+typeStr)
		}
	}
	return strings.Join(parts, ", ")
}

func formatResultList(fl *ast.FieldList, fset *token.FileSet) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	for _, field := range fl.List {
		typeStr := nodeString(field.Type, fset)
		if len(field.Names) == 0 {
			parts = append(parts, typeStr)
		} else {
			names := make([]string, len(field.Names))
			for i, n := range field.Names {
				names[i] = n.Name
			}
			parts = append(parts, strings.Join(names, ", ")+" "+typeStr)
		}
	}
	return strings.Join(parts, ", ")
}

func paramNames(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var names []string
	paramIdx := 0
	for _, field := range fl.List {
		isVariadic := false
		if _, ok := field.Type.(*ast.Ellipsis); ok {
			isVariadic = true
		}
		if len(field.Names) == 0 {
			name := fmt.Sprintf("p%d", paramIdx)
			paramIdx++
			if isVariadic {
				names = append(names, name+"...")
			} else {
				names = append(names, name)
			}
		} else {
			for _, n := range field.Names {
				name := n.Name
				if name == "_" {
					name = fmt.Sprintf("p%d", paramIdx)
				}
				if isVariadic {
					names = append(names, name+"...")
				} else {
					names = append(names, name)
				}
				paramIdx++
			}
		}
	}
	return strings.Join(names, ", ")
}

func nodeString(node ast.Node, fset *token.FileSet) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return "<error>"
	}
	return buf.String()
}
