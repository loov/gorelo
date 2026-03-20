package relo

import (
	"go/ast"
	"go/token"

	"github.com/loov/gorelo/mast"
)

// computeDetachEdits generates edits for detaching methods (converting to
// standalone functions) and attaching functions (converting to methods).
// Rename edits for these groups are skipped in computeRenames and handled
// here as part of the structural transformation.
func computeDetachEdits(ix *mast.Index, resolved []*resolvedRelo, renames *renameSet, plan *Plan) {
	for _, rr := range resolved {
		switch {
		case rr.Relo.Detach:
			detachMethod(ix, rr, renames, plan)
		case rr.Relo.MethodOf != "":
			attachMethod(ix, rr, renames, plan)
		}
	}
}

// detachMethod converts a method to a standalone function.
func detachMethod(ix *mast.Index, rr *resolvedRelo, renames *renameSet, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil || fd.Recv == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find method declaration for %s", rr.Group.Name)
		return
	}

	detachDecl(ix, rr, fd, renames)
	detachCallSites(ix, rr, fd, renames, plan)
}

// detachDecl rewrites the declaration:
// func (s *Server) Start(ctx context.Context) error
// → func Start(s *Server, ctx context.Context) error
// If renamed, the name ident is also changed.
func detachDecl(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet) {
	fset := ix.Fset
	filePath := rr.File.Path

	// Remove receiver: from opening paren to closing paren + trailing space.
	recvOpen := fset.Position(fd.Recv.Opening).Offset
	recvClose := fset.Position(fd.Recv.Closing).Offset

	src := fileContent(rr.File)
	recvEnd := recvClose + 1
	for recvEnd < len(src) && src[recvEnd] == ' ' {
		recvEnd++
	}

	renames.byFile[filePath] = append(renames.byFile[filePath], edit{
		Start: recvOpen,
		End:   recvEnd,
		New:   "",
	})

	// If renamed, change the name ident.
	if rr.TargetName != rr.Group.Name {
		nameStart := fset.Position(fd.Name.Pos()).Offset
		nameEnd := nameStart + len(fd.Name.Name)
		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: nameStart,
			End:   nameEnd,
			New:   rr.TargetName,
		})
	}

	// Insert receiver as first parameter.
	recvParam := formatRecvAsParam(fd.Recv, fset)
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	hasParams := fd.Type.Params != nil && len(fd.Type.Params.List) > 0

	insertText := recvParam
	if hasParams {
		insertText += ", "
	}

	renames.byFile[filePath] = append(renames.byFile[filePath], edit{
		Start: paramsOpen + 1,
		End:   paramsOpen + 1,
		New:   insertText,
	})
}

// detachCallSites rewrites call sites from s.Method(args) → Func(s, args).
func detachCallSites(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet, plan *Plan) {
	newName := rr.TargetName

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

		// Replace "recv.Method" with the new name, removing the receiver prefix.
		selStart := fset.Position(sel.Sel.Pos()).Offset
		selEnd := selStart + len(sel.Sel.Name)
		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: xStart,
			End:   selEnd,
			New:   newName,
		})

		if call != nil {
			lparen := fset.Position(call.Lparen).Offset
			hasArgs := len(call.Args) > 0

			insertText := recvText
			if hasArgs {
				insertText += ", "
			}

			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1,
				End:   lparen + 1,
				New:   insertText,
			})
		} else {
			plan.Warnings.Addf(
				"method value reference to %s.%s will change signature after detach",
				recvText, rr.Group.Name)
		}
	}
}

// attachMethod converts a standalone function to a method.
func attachMethod(ix *mast.Index, rr *resolvedRelo, renames *renameSet, plan *Plan) {
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

	attachDecl(ix, rr, fd, renames)
	attachCallSites(ix, rr, fd, renames, plan)
}

// attachDecl rewrites the declaration:
// func StartServer(s *Server, ctx context.Context) error
// → func (s *Server) Start(ctx context.Context) error
// The receiver insertion and optional rename are combined into a single
// edit at the name position to avoid conflicting with separate rename edits.
func attachDecl(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet) {
	fset := ix.Fset
	filePath := rr.File.Path
	src := fileContent(rr.File)

	firstField := fd.Type.Params.List[0]

	// Build receiver text from first parameter.
	paramStart := fset.Position(firstField.Pos()).Offset
	paramEnd := firstParamEnd(fset, fd, src)
	recvText := string(src[paramStart:paramEnd])

	// Replace function name with receiver + name (possibly renamed).
	nameStart := fset.Position(fd.Name.Pos()).Offset
	nameEnd := nameStart + len(fd.Name.Name)
	renames.byFile[filePath] = append(renames.byFile[filePath], edit{
		Start: nameStart,
		End:   nameEnd,
		New:   "(" + recvText + ") " + rr.TargetName,
	})

	// Remove first parameter from parameter list.
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	removeEnd := paramEnd
	if len(fd.Type.Params.List) > 1 {
		nextStart := fset.Position(fd.Type.Params.List[1].Pos()).Offset
		removeEnd = nextStart
	}

	renames.byFile[filePath] = append(renames.byFile[filePath], edit{
		Start: paramsOpen + 1,
		End:   removeEnd,
		New:   "",
	})
}

// attachCallSites rewrites call sites from Func(s, args) → s.Method(args).
// The function name replacement includes the receiver prefix and optional
// rename as a single edit.
func attachCallSites(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet, plan *Plan) {
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

		// Extract first argument text (the receiver).
		firstArg := call.Args[0]
		argStart := fset.Position(firstArg.Pos()).Offset
		argEnd := fset.Position(firstArg.End()).Offset
		recvText := string(src[argStart:argEnd])

		// Replace the function name (with optional qualifier) with recv.Method.
		identStart := fset.Position(id.Ident.Pos()).Offset
		editStart := identStart
		if id.Qualifier != nil {
			editStart = fset.Position(id.Qualifier.Pos()).Offset
		}

		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: editStart,
			End:   identStart + len(id.Ident.Name),
			New:   recvText + "." + newName,
		})

		// Remove first argument.
		lparen := fset.Position(call.Lparen).Offset
		if len(call.Args) > 1 {
			secondArg := call.Args[1]
			secondStart := fset.Position(secondArg.Pos()).Offset
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1,
				End:   secondStart,
				New:   "",
			})
		} else {
			rparen := fset.Position(call.Rparen).Offset
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1,
				End:   rparen,
				New:   "",
			})
		}
	}
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
func formatRecvAsParam(recv *ast.FieldList, fset *token.FileSet) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	field := recv.List[0]
	typeStr := nodeString(field.Type, fset)
	if len(field.Names) > 0 {
		return field.Names[0].Name + " " + typeStr
	}
	return typeStr
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

// firstParamEnd returns the byte offset of the end of the first parameter
// in the function's parameter list.
func firstParamEnd(fset *token.FileSet, fd *ast.FuncDecl, _ []byte) int {
	return fset.Position(fd.Type.Params.List[0].End()).Offset
}
