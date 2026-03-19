package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"unicode"

	"github.com/loov/gorelo/mast"
)

// resolvedRelo is a validated and enriched relo.
type resolvedRelo struct {
	Relo        Relo
	Group       *mast.Group
	DefIdent    *mast.Ident
	File        *mast.File
	TargetFile  string
	TargetName  string
	Synthesized bool
}

// resolve validates, deduplicates, and synthesizes relos (phases 0-1).
func resolve(ix *mast.Index, relos []Relo, plan *Plan) ([]*resolvedRelo, error) {
	// Phase 0: validate each relo.
	seen := make(map[*mast.Group]*resolvedRelo)
	var resolved []*resolvedRelo

	for _, r := range relos {
		grp := ix.Group(r.Ident)
		if grp == nil {
			return nil, fmt.Errorf("ident %q at %v is not tracked by the index", r.Ident.Name, r.Ident.Pos())
		}

		// Find the Def ident in the group that matches this ast.Ident.
		var defIdent *mast.Ident
		for _, id := range grp.Idents {
			if id.Ident == r.Ident && id.Kind == mast.Def {
				defIdent = id
				break
			}
		}
		if defIdent == nil {
			// The provided ident might not be the def itself;
			// find any def in the group.
			for _, id := range grp.Idents {
				if id.Kind == mast.Def {
					defIdent = id
					break
				}
			}
		}
		if defIdent == nil {
			return nil, fmt.Errorf("no definition found for ident %q", r.Ident.Name)
		}

		// Validate kind.
		switch grp.Kind {
		case mast.TypeName, mast.Func, mast.Method, mast.Const, mast.Var:
			// OK for both MoveTo and Rename.
		case mast.Field:
			if r.MoveTo != "" {
				return nil, fmt.Errorf("field %q cannot be moved, only renamed", grp.Name)
			}
		case mast.Label, mast.PackageName, mast.Unknown:
			return nil, fmt.Errorf("cannot relocate %q (kind %d)", grp.Name, grp.Kind)
		}

		// Validate rename target is a valid Go identifier.
		if r.Rename != "" && !token.IsIdentifier(r.Rename) {
			return nil, fmt.Errorf("rename target %q is not a valid Go identifier", r.Rename)
		}

		// Unexported cross-package check.
		if r.MoveTo != "" && defIdent.File != nil {
			srcPkg := defIdent.File.Pkg
			if srcPkg != nil && !isSamePackageDir(srcPkg, r.MoveTo) {
				name := r.Rename
				if name == "" {
					name = grp.Name
				}
				if len(name) > 0 && !unicode.IsUpper(rune(name[0])) {
					return nil, fmt.Errorf("unexported name %q cannot be moved cross-package without a rename to an exported name", grp.Name)
				}
			}
		}

		// Deduplicate by group.
		if existing, ok := seen[grp]; ok {
			// If the existing relo is synthesized and this one is explicit, prefer explicit.
			if existing.Synthesized {
				existing.Relo = r
				existing.TargetFile = r.MoveTo
				if r.Rename != "" {
					existing.TargetName = r.Rename
				}
				existing.Synthesized = false
				continue
			}
			// Two explicit relos for the same group: error if they conflict.
			newTarget := r.MoveTo
			if newTarget == "" && defIdent.File != nil {
				newTarget = defIdent.File.Path
			}
			newName := r.Rename
			if newName == "" {
				newName = grp.Name
			}
			if existing.TargetFile != newTarget || existing.TargetName != newName {
				return nil, fmt.Errorf("conflicting relos for %q: (%s, %s) vs (%s, %s)",
					grp.Name, existing.TargetFile, existing.TargetName, newTarget, newName)
			}
			// Identical — silently deduplicate.
			continue
		}

		rr := &resolvedRelo{
			Relo:       r,
			Group:      grp,
			DefIdent:   defIdent,
			File:       defIdent.File,
			TargetFile: r.MoveTo,
			TargetName: grp.Name,
		}
		if r.Rename != "" {
			rr.TargetName = r.Rename
		}
		if rr.TargetFile == "" && rr.File != nil {
			rr.TargetFile = rr.File.Path
		}

		seen[grp] = rr
		resolved = append(resolved, rr)
	}

	// Phase 1: synthesize additional relos.
	resolved = synthesize(ix, resolved, seen, plan)

	return resolved, nil
}

// synthesize adds methods for moved types and generates warnings.
func synthesize(ix *mast.Index, resolved []*resolvedRelo, seen map[*mast.Group]*resolvedRelo, plan *Plan) []*resolvedRelo {
	// Collect moved type names for method synthesis and warnings.
	movedTypes := make(map[string]*resolvedRelo) // "pkg.TypeName" -> relo
	movedNames := make(map[string]*resolvedRelo) // "pkg.Name" -> relo

	for _, rr := range resolved {
		key := rr.Group.Pkg + "." + rr.Group.Name
		movedNames[key] = rr
		if rr.Group.Kind == mast.TypeName {
			movedTypes[key] = rr
		}
	}

	// For each moved type, find and auto-add its methods.
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil {
					continue
				}
				recvType := receiverTypeName(fd.Recv)
				if recvType == "" {
					continue
				}
				typeKey := pkg.Path + "." + recvType
				typeRelo, ok := movedTypes[typeKey]
				if !ok {
					continue
				}

				// Find the group for this method's name ident.
				grp := ix.Group(fd.Name)
				if grp == nil {
					continue
				}
				if _, already := seen[grp]; already {
					continue
				}

				// Find the def ident.
				var defIdent *mast.Ident
				for _, id := range grp.Idents {
					if id.Kind == mast.Def {
						defIdent = id
						break
					}
				}
				if defIdent == nil {
					continue
				}

				rr := &resolvedRelo{
					Relo: Relo{
						Ident:  fd.Name,
						MoveTo: typeRelo.TargetFile,
					},
					Group:       grp,
					DefIdent:    defIdent,
					File:        defIdent.File,
					TargetFile:  typeRelo.TargetFile,
					TargetName:  grp.Name,
					Synthesized: true,
				}
				seen[grp] = rr
				resolved = append(resolved, rr)
			}
		}
	}

	// Warnings.
	for _, typeRelo := range movedTypes {
		// Warn about NewT constructors.
		ctorKey := typeRelo.Group.Pkg + ".New" + typeRelo.Group.Name
		if _, moved := movedNames[ctorKey]; !moved {
			// Check if the constructor exists.
			for _, pkg := range ix.Pkgs {
				if pkg.Path != typeRelo.Group.Pkg {
					continue
				}
				for _, file := range pkg.Files {
					for _, decl := range file.Syntax.Decls {
						fd, ok := decl.(*ast.FuncDecl)
						if !ok || fd.Recv != nil {
							continue
						}
						if fd.Name.Name == "New"+typeRelo.Group.Name {
							plan.Warnings.AddAtf(typeRelo, ix,
								"constructor New%s exists but is not being moved with type %s",
								typeRelo.Group.Name, typeRelo.Group.Name)
						}
					}
				}
			}
		}
	}

	// Warn about orphaned methods.
	for _, rr := range resolved {
		if rr.Group.Kind != mast.Method {
			continue
		}
		// Find the receiver type name from the method's function decl.
		recvType := findMethodReceiverType(rr)
		if recvType == "" {
			continue
		}
		typeKey := rr.Group.Pkg + "." + recvType
		if _, ok := movedTypes[typeKey]; !ok {
			plan.Warnings.AddAtf(rr, ix,
				"method %s.%s is being moved but type %s is not",
				recvType, rr.Group.Name, recvType)
		}
	}

	return resolved
}

// findMethodReceiverType finds the receiver type name for a method relo
// by scanning the file's AST for the matching FuncDecl.
func findMethodReceiverType(rr *resolvedRelo) string {
	if rr.File == nil {
		return ""
	}
	for _, decl := range rr.File.Syntax.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil {
			continue
		}
		if fd.Name == rr.DefIdent.Ident {
			return receiverTypeName(fd.Recv)
		}
	}
	return ""
}

// receiverTypeName extracts the type name from a method receiver field list.
func receiverTypeName(recv *ast.FieldList) string {
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

// isSamePackageDir checks if targetFile is in the same directory as pkg.
func isSamePackageDir(pkg *mast.Package, targetFile string) bool {
	if len(pkg.Files) == 0 {
		return false
	}
	return dirOf(pkg.Files[0].Path) == dirOf(targetFile)
}
