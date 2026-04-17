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

// aliasResult holds generated stubs and any import alias needed.
type aliasResult struct {
	Stubs    []string
	Warnings Warnings
	// ImportAlias is non-empty when a parameter name shadows targetPkgName,
	// requiring an aliased import (e.g., _newpkg).
	ImportAlias string
}

// generateAliases generates //go:fix inline stubs for cross-package moves.
func generateAliases(rrs []*resolvedRelo, targetPkgName string, fset *token.FileSet) aliasResult {
	var result aliasResult
	pkgQualifier := targetPkgName

	// Check whether any func parameter shadows targetPkgName.
	if funcParamShadows(rrs, targetPkgName) {
		pkgQualifier = "_" + targetPkgName
		result.ImportAlias = pkgQualifier
		result.Warnings.Addf("parameter name %q shadows target package; using import alias %q in stubs",
			targetPkgName, pkgQualifier)
	}

	for _, rr := range rrs {
		switch rr.Group.Kind {
		case mast.TypeName:
			result.Stubs = append(result.Stubs, generateTypeAlias(rr, pkgQualifier, fset))
		case mast.Func:
			result.Stubs = append(result.Stubs, generateFuncAlias(rr, pkgQualifier, fset))
		case mast.Const:
			result.Stubs = append(result.Stubs, fmt.Sprintf("//go:fix inline\nconst %s = %s.%s",
				rr.Group.Name, pkgQualifier, rr.TargetName))
		case mast.Var:
			result.Stubs = append(result.Stubs, fmt.Sprintf("// Deprecated: Use %s.%s instead.\nvar %s = %s.%s",
				pkgQualifier, rr.TargetName, rr.Group.Name, pkgQualifier, rr.TargetName))
		case mast.Method:
			// Methods follow receiver type alias; no separate stub.
		}
	}

	return result
}

// funcParamShadows reports whether any func relo in rrs has a parameter
// whose name equals targetPkgName.
func funcParamShadows(rrs []*resolvedRelo, targetPkgName string) bool {
	for _, rr := range rrs {
		if rr.Group.Kind != mast.Func || rr.File == nil {
			continue
		}
		fd, ok := rr.enclosingDecl().(*ast.FuncDecl)
		if !ok || fd.Type.Params == nil {
			continue
		}
		for _, field := range fd.Type.Params.List {
			for _, name := range field.Names {
				if name.Name == targetPkgName {
					return true
				}
			}
		}
	}
	return false
}

func generateTypeAlias(rr *resolvedRelo, targetPkgName string, fset *token.FileSet) string {
	// Check for type parameters.
	if gd, ok := rr.enclosingDecl().(*ast.GenDecl); ok {
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
	fd, ok := rr.enclosingDecl().(*ast.FuncDecl)
	if !ok {
		return fmt.Sprintf("// TODO: alias for %s", rr.Group.Name)
	}

	var buf bytes.Buffer
	buf.WriteString("//go:fix inline\nfunc ")
	buf.WriteString(rr.Group.Name)

	// B2: Handle type parameters for generic functions.
	if fd.Type.TypeParams != nil && len(fd.Type.TypeParams.List) > 0 {
		buf.WriteString("[")
		buf.WriteString(formatTypeParams(fd.Type.TypeParams, fset))
		buf.WriteString("]")
	}

	buf.WriteString("(")
	buf.WriteString(formatFieldList(fd.Type.Params, fset, true))
	buf.WriteString(")")

	hasResults := fd.Type.Results != nil && len(fd.Type.Results.List) > 0
	if hasResults {
		results := formatFieldList(fd.Type.Results, fset, false)
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

	// B2: Include type arguments in the forwarding call.
	if fd.Type.TypeParams != nil && len(fd.Type.TypeParams.List) > 0 {
		buf.WriteString("[")
		buf.WriteString(typeParamNames(fd.Type.TypeParams))
		buf.WriteString("]")
	}

	buf.WriteString("(")
	buf.WriteString(paramNames(fd.Type.Params))
	buf.WriteString(") }")

	return buf.String()
}

// typeParamNames extracts just the names (no constraints) from a type parameter list.
func typeParamNames(fl *ast.FieldList) string {
	var names []string
	for _, field := range fl.List {
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return strings.Join(names, ", ")
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

// formatFieldList formats an ast.FieldList as a comma-separated
// string. When nameUnnamed is true, unnamed or underscore parameters
// get synthetic names (p0, p1, …) so the output is valid in a
// function signature that needs to forward arguments by name.
func formatFieldList(fl *ast.FieldList, fset *token.FileSet, nameUnnamed bool) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	paramIdx := 0
	for _, field := range fl.List {
		typeStr := nodeString(field.Type, fset)
		if len(field.Names) == 0 {
			if nameUnnamed {
				name := fmt.Sprintf("p%d", paramIdx)
				parts = append(parts, name+" "+typeStr)
			} else {
				parts = append(parts, typeStr)
			}
			paramIdx++
		} else {
			names := make([]string, len(field.Names))
			for i, n := range field.Names {
				if nameUnnamed && n.Name == "_" {
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
