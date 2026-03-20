package relo

import (
	"go/ast"
	"go/token"
	"path/filepath"

	"github.com/loov/gorelo/mast"
)

// computeDetachEdits generates edits for detaching methods (converting to
// standalone functions) and attaching functions (converting to methods).
// Declaration edits are stored in the renameSet (for same-file application)
// and call-site edits are also merged into renames.
//
// For cross-file moves, declaration edits must also be applied during span
// extraction — see structuralDeclEdits used in assembleTargets.
func computeDetachEdits(ix *mast.Index, resolved []*resolvedRelo, renames *renameSet, imports *importSet, plan *Plan) {
	for _, rr := range resolved {
		switch {
		case rr.Relo.Detach:
			detachMethod(ix, rr, renames, imports, plan)
		case rr.Relo.MethodOf != "":
			attachMethod(ix, rr, renames, imports, plan)
		}
	}
}

// detachMethod converts a method to a standalone function.
func detachMethod(ix *mast.Index, rr *resolvedRelo, renames *renameSet, imports *importSet, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil || fd.Recv == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find method declaration for %s", rr.Group.Name)
		return
	}

	// Add declaration edits to renameSet (used for same-file operations;
	// filtered out for cross-file moves, which use structuralDeclEdits).
	filePath := rr.File.Path
	renames.byFile[filePath] = append(renames.byFile[filePath], detachDeclEdits(ix, rr, fd)...)

	// For cross-package moves, the detached function's parameter references
	// the receiver type from the source package. Add the source import
	// to the target file.
	if rr.isCrossFileMove() {
		srcDir := filepath.Dir(rr.File.Path)
		tgtDir := filepath.Dir(rr.TargetFile)
		if srcDir != tgtDir {
			srcImportPath := guessImportPath(srcDir)
			if srcImportPath != "" {
				addImportToFile(imports, ix, rr.TargetFile, srcImportPath)
			}
		}
	}

	detachCallSites(ix, rr, fd, renames, imports, plan)
}

// detachDeclEdits returns edits to convert a method declaration to a function.
// Edits are in absolute file offsets.
func detachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	// Remove receiver: from opening paren to closing paren + trailing space.
	recvOpen := fset.Position(fd.Recv.Opening).Offset
	recvClose := fset.Position(fd.Recv.Closing).Offset
	recvEnd := recvClose + 1
	for recvEnd < len(src) && src[recvEnd] == ' ' {
		recvEnd++
	}
	edits = append(edits, edit{Start: recvOpen, End: recvEnd, New: ""})

	// Rename ident if needed.
	if rr.TargetName != rr.Group.Name {
		nameStart := fset.Position(fd.Name.Pos()).Offset
		nameEnd := nameStart + len(fd.Name.Name)
		edits = append(edits, edit{Start: nameStart, End: nameEnd, New: rr.TargetName})
	}

	// Insert receiver as first parameter.
	recvParam := formatRecvAsParam(fd.Recv, fset, "")
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	hasParams := fd.Type.Params != nil && len(fd.Type.Params.List) > 0
	insertText := recvParam
	if hasParams {
		insertText += ", "
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: paramsOpen + 1, New: insertText})

	return edits
}

// detachCallSites rewrites call sites from s.Method(args) → Func(s, args)
// or pkg.Func(s, args) when moving cross-package.
func detachCallSites(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet, imports *importSet, plan *Plan) {
	newName := rr.TargetName

	// Determine cross-package qualification.
	var tgtDir string
	var tgtImportPath string
	var tgtLocalName string
	if rr.File != nil && rr.TargetFile != "" {
		srcDir := filepath.Dir(rr.File.Path)
		tgtDir = filepath.Dir(rr.TargetFile)
		if srcDir != tgtDir {
			tgtImportPath = guessImportPath(tgtDir)
			if tgtImportPath != "" {
				tgtLocalName = guessImportLocalName(tgtImportPath)
			}
		}
	}

	for _, id := range rr.Group.Idents {
		if id.Kind != mast.Use || id.File == nil {
			continue
		}
		sel, call := enclosingCallExpr(id.File.Syntax, id.Ident)
		if sel == nil {
			continue
		}

		filePath := id.File.Path
		fset := ix.Fset
		src := fileContent(id.File)

		xStart := fset.Position(sel.X.Pos()).Offset
		xEnd := fset.Position(sel.X.End()).Offset
		recvText := string(src[xStart:xEnd])

		// Determine the qualified function name for this call site.
		qualName := newName
		if tgtImportPath != "" {
			callDir := filepath.Dir(filePath)
			if callDir != tgtDir {
				qualName = tgtLocalName + "." + newName
				// Add import for the target package.
				addImportToFile(imports, ix, filePath, tgtImportPath)
			}
		}

		// Replace "recv.Method" with the (possibly qualified) function name.
		selStart := fset.Position(sel.Sel.Pos()).Offset
		selEnd := selStart + len(sel.Sel.Name)
		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: xStart, End: selEnd, New: qualName,
		})

		if call != nil {
			lparen := fset.Position(call.Lparen).Offset
			hasArgs := len(call.Args) > 0
			insertText := recvText
			if hasArgs {
				insertText += ", "
			}
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1, End: lparen + 1, New: insertText,
			})
		} else {
			plan.Warnings.Addf(
				"method value reference to %s.%s will change signature after detach",
				recvText, rr.Group.Name)
		}
	}
}

// attachMethod converts a standalone function to a method.
func attachMethod(ix *mast.Index, rr *resolvedRelo, renames *renameSet, imports *importSet, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find function declaration for %s", rr.Group.Name)
		return
	}
	if fd.Recv != nil {
		plan.Warnings.AddAtf(rr, ix, "%s is already a method", rr.Group.Name)
		return
	}
	if fd.Type.TypeParams != nil && len(fd.Type.TypeParams.List) > 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: generic functions cannot become methods", rr.Group.Name)
		return
	}
	if fd.Type.Params == nil || len(fd.Type.Params.List) == 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: no parameters", rr.Group.Name)
		return
	}

	firstField := fd.Type.Params.List[0]
	if _, isEllipsis := firstField.Type.(*ast.Ellipsis); isEllipsis {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: first parameter is variadic", rr.Group.Name)
		return
	}
	if len(firstField.Names) == 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: first parameter has no name", rr.Group.Name)
		return
	}

	recvTypeName := typeExprName(firstField.Type)
	if recvTypeName != rr.Relo.MethodOf {
		plan.Warnings.AddAtf(rr, ix,
			"cannot attach %s as method on %s: first parameter type is %s",
			rr.Group.Name, rr.Relo.MethodOf, recvTypeName)
		return
	}

	// Add declaration edits to renameSet.
	filePath := rr.File.Path
	renames.byFile[filePath] = append(renames.byFile[filePath], attachDeclEdits(ix, rr, fd)...)

	attachCallSites(ix, rr, fd, renames, imports, plan)
}

// attachDeclEdits returns edits to convert a function declaration to a method.
// Edits are in absolute file offsets.
func attachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	firstField := fd.Type.Params.List[0]
	paramStart := fset.Position(firstField.Pos()).Offset
	paramEnd := fset.Position(firstField.End()).Offset
	recvText := string(src[paramStart:paramEnd])

	// Replace function name with receiver + name (possibly renamed).
	nameStart := fset.Position(fd.Name.Pos()).Offset
	nameEnd := nameStart + len(fd.Name.Name)
	edits = append(edits, edit{
		Start: nameStart, End: nameEnd,
		New: "(" + recvText + ") " + rr.TargetName,
	})

	// Remove first parameter from parameter list.
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	removeEnd := paramEnd
	if len(fd.Type.Params.List) > 1 {
		nextStart := fset.Position(fd.Type.Params.List[1].Pos()).Offset
		removeEnd = nextStart
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: removeEnd, New: ""})

	return edits
}

// attachCallSites rewrites call sites from Func(s, args) → s.Method(args).
func attachCallSites(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet, imports *importSet, plan *Plan) {
	newName := rr.TargetName

	for _, id := range rr.Group.Idents {
		if id.Kind != mast.Use || id.File == nil {
			continue
		}

		filePath := id.File.Path
		fset := ix.Fset
		src := fileContent(id.File)

		call := enclosingCallOnly(id.File.Syntax, id.Ident)
		if call == nil || len(call.Args) == 0 {
			continue
		}

		firstArg := call.Args[0]
		argStart := fset.Position(firstArg.Pos()).Offset
		argEnd := fset.Position(firstArg.End()).Offset
		recvText := string(src[argStart:argEnd])

		identStart := fset.Position(id.Ident.Pos()).Offset
		editStart := identStart
		if id.Qualifier != nil {
			editStart = fset.Position(id.Qualifier.Pos()).Offset
		}

		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: editStart, End: identStart + len(id.Ident.Name),
			New: recvText + "." + newName,
		})

		lparen := fset.Position(call.Lparen).Offset
		if len(call.Args) > 1 {
			secondArg := call.Args[1]
			secondStart := fset.Position(secondArg.Pos()).Offset
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1, End: secondStart, New: "",
			})
		} else {
			rparen := fset.Position(call.Rparen).Offset
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1, End: rparen, New: "",
			})
		}
	}
}

// addImportToFile adds an import for impPath to the given file in the import set,
// checking if it's already present.
func addImportToFile(imports *importSet, ix *mast.Index, filePath, impPath string) {
	ic := imports.ensureFile(filePath)
	// Check if already being added.
	for _, entry := range ic.Add {
		if entry.Path == impPath {
			return
		}
	}
	// Check if already imported.
	if f := ix.FilesByPath[filePath]; f != nil {
		for _, imp := range f.Syntax.Imports {
			if importPath(imp) == impPath {
				return
			}
		}
	}
	ic.Add = append(ic.Add, importEntry{Path: impPath})
}

// structuralDeclEdits computes span-relative edits for the declaration
// of a detach/attach relo. Used during span extraction in assembleTargets.
func structuralDeclEdits(ix *mast.Index, rr *resolvedRelo, s *span) []edit {
	if rr.File == nil || s == nil {
		return nil
	}

	var absEdits []edit

	if rr.Relo.Detach {
		fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
		if fd == nil || fd.Recv == nil {
			return nil
		}
		// For cross-package moves, qualify the receiver type.
		absEdits = detachDeclEditsTarget(ix, rr, fd)
	} else if rr.Relo.MethodOf != "" {
		fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
		if fd == nil {
			return nil
		}
		absEdits = attachDeclEdits(ix, rr, fd)
	}

	// Convert to span-relative offsets.
	var relEdits []edit
	for _, e := range absEdits {
		relEdits = append(relEdits, edit{
			Start: e.Start - s.Start,
			End:   e.End - s.Start,
			New:   e.New,
		})
	}
	return relEdits
}

// detachDeclEditsTarget is like detachDeclEdits but qualifies the receiver
// type for cross-package moves.
func detachDeclEditsTarget(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	// Remove receiver.
	recvOpen := fset.Position(fd.Recv.Opening).Offset
	recvClose := fset.Position(fd.Recv.Closing).Offset
	recvEnd := recvClose + 1
	for recvEnd < len(src) && src[recvEnd] == ' ' {
		recvEnd++
	}
	edits = append(edits, edit{Start: recvOpen, End: recvEnd, New: ""})

	// Rename ident if needed.
	if rr.TargetName != rr.Group.Name {
		nameStart := fset.Position(fd.Name.Pos()).Offset
		nameEnd := nameStart + len(fd.Name.Name)
		edits = append(edits, edit{Start: nameStart, End: nameEnd, New: rr.TargetName})
	}

	// Determine package qualifier for the receiver type.
	var pkgQualifier string
	srcDir := filepath.Dir(rr.File.Path)
	tgtDir := filepath.Dir(rr.TargetFile)
	if srcDir != tgtDir {
		srcImportPath := guessImportPath(srcDir)
		if srcImportPath != "" {
			pkgQualifier = guessImportLocalName(srcImportPath)
		}
	}

	// Insert receiver as first parameter (possibly qualified).
	recvParam := formatRecvAsParam(fd.Recv, fset, pkgQualifier)
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	hasParams := fd.Type.Params != nil && len(fd.Type.Params.List) > 0
	insertText := recvParam
	if hasParams {
		insertText += ", "
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: paramsOpen + 1, New: insertText})

	return edits
}

// findFuncDecl returns the FuncDecl whose Name matches ident.
func findFuncDecl(file *ast.File, ident *ast.Ident) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name == ident {
			return fd
		}
	}
	return nil
}

// enclosingCallExpr finds the SelectorExpr containing ident as Sel,
// and optionally the enclosing CallExpr if it's being called.
func enclosingCallExpr(file *ast.File, ident *ast.Ident) (sel *ast.SelectorExpr, call *ast.CallExpr) {
	ast.Inspect(file, func(n ast.Node) bool {
		if sel != nil {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			if s, ok := x.Fun.(*ast.SelectorExpr); ok && s.Sel == ident {
				sel = s
				call = x
				return false
			}
		case *ast.SelectorExpr:
			if x.Sel == ident {
				sel = x
				return false
			}
		}
		return true
	})
	return
}

// enclosingCallOnly finds the CallExpr where ident is the function being called.
func enclosingCallOnly(file *ast.File, ident *ast.Ident) *ast.CallExpr {
	var result *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun == ident {
				result = call
				return false
			}
		case *ast.SelectorExpr:
			if fun.Sel == ident {
				result = call
				return false
			}
		}
		return true
	})
	return result
}

// formatRecvAsParam formats a receiver field list as a parameter string.
// If pkgQualifier is non-empty, the receiver type is qualified (e.g., "s *pkg.Server").
func formatRecvAsParam(recv *ast.FieldList, fset *token.FileSet, pkgQualifier string) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	field := recv.List[0]
	typeStr := nodeString(field.Type, fset)
	if pkgQualifier != "" {
		typeStr = qualifyTypeStr(typeStr, pkgQualifier)
	}
	if len(field.Names) > 0 {
		return field.Names[0].Name + " " + typeStr
	}
	return typeStr
}

// qualifyTypeStr prepends a package qualifier to a type string,
// handling pointer indirection (e.g., "*Server" → "*pkg.Server").
func qualifyTypeStr(typeStr, pkg string) string {
	if len(typeStr) > 0 && typeStr[0] == '*' {
		return "*" + pkg + "." + typeStr[1:]
	}
	return pkg + "." + typeStr
}

// typeExprName returns the base type name from a type expression,
// stripping pointer indirection.
func typeExprName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}
