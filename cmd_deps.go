package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/zeebo/clingy"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

// cmdDeps shows what a declaration depends on.

type cmdDeps struct {
	jsonOutput bool
	args       []string
}

func (c *cmdDeps) Setup(params clingy.Parameters) {
	c.jsonOutput = params.Flag("json", "emit JSON output", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.args = params.Arg("specifier", "declaration specifier (e.g. Server, ./pkg.Name, file.go:Name)",
		clingy.Repeated).([]string)
}

func (c *cmdDeps) Execute(ctx context.Context) error {
	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	var results []depsResult
	for _, arg := range c.args {
		r, err := resolveDeps(ix, absDir, arg)
		if err != nil {
			return err
		}
		results = append(results, r)
	}

	w := clingy.Stdout(ctx)
	if c.jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	for i, r := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  (%s, %s:%d)\n", r.Name, r.Kind, r.DefFile, r.DefLine)
		for _, dep := range r.Deps {
			loc := dep.File + ":" + strconv.Itoa(dep.Line)
			fmt.Fprintf(w, "  %-7s %-30s %s\n", dep.Kind, dep.Name, loc)
		}
		if len(r.Deps) == 0 {
			fmt.Fprintln(w, "  (no dependencies)")
		}
	}
	return nil
}

type depsResult struct {
	Name    string      `json:"name"`
	Kind    string      `json:"kind"`
	DefFile string      `json:"def_file"`
	DefLine int         `json:"def_line"`
	Deps    []depsEntry `json:"deps"`
}

type depsEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

func resolveDeps(ix *mast.Index, absDir string, arg string) (depsResult, error) {
	item, err := rules.ParseItem(arg)
	if err != nil {
		return depsResult{}, fmt.Errorf("parsing %q: %w", arg, err)
	}

	source := relo.ResolveSource(ix, item.Source, absDir)

	id := ix.FindDef(item.Name, source)
	if id == nil {
		src := ""
		if item.Source != "" {
			src = " in " + item.Source
		}
		return depsResult{}, fmt.Errorf("could not find %q%s", item.Name, src)
	}

	grp := ix.Group(id)
	if grp == nil {
		return depsResult{}, fmt.Errorf("no group for %q", arg)
	}

	defIdent := grp.DefIdent()
	if defIdent == nil || defIdent.File == nil {
		return depsResult{}, fmt.Errorf("no definition found for %q", arg)
	}

	// Find the enclosing AST declaration node.
	declNode := findDeclNode(defIdent.File.Syntax, id)
	if declNode == nil {
		return depsResult{}, fmt.Errorf("could not find declaration node for %q", arg)
	}

	// Walk the declaration AST and collect referenced groups.
	seen := map[*mast.Group]bool{grp: true} // exclude self
	var deps []depsEntry

	ast.Inspect(declNode, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		ref := ix.Group(ident)
		if ref == nil || seen[ref] || !ref.IsPackageScope() {
			return true
		}
		seen[ref] = true

		def := ref.DefIdent()
		if def == nil || def.File == nil {
			return true
		}

		deps = append(deps, depsEntry{
			Name: ref.Name,
			Kind: objectKindString(ref.Kind),
			File: relPath(absDir, def.File.Path),
			Line: ix.Fset.Position(def.Ident.Pos()).Line,
		})
		return true
	})

	// Also collect dependencies from methods if the specifier is a type.
	if grp.Kind == mast.TypeName {
		collectMethodDeps(ix, grp, absDir, seen, &deps)
	}

	sort.Slice(deps, func(i, j int) bool {
		if deps[i].File != deps[j].File {
			return deps[i].File < deps[j].File
		}
		return deps[i].Line < deps[j].Line
	})

	return depsResult{
		Name:    grp.Name,
		Kind:    objectKindString(grp.Kind),
		DefFile: relPath(absDir, defIdent.File.Path),
		DefLine: ix.Fset.Position(defIdent.Ident.Pos()).Line,
		Deps:    deps,
	}, nil
}

// collectMethodDeps walks all methods of a type and adds their dependencies.
func collectMethodDeps(ix *mast.Index, typeGrp *mast.Group, absDir string, seen map[*mast.Group]bool, deps *[]depsEntry) {
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil {
					continue
				}
				if mast.ReceiverTypeName(fd.Recv) != typeGrp.Name {
					continue
				}
				// Verify it's the same type by checking the group.
				methodGrp := ix.Group(fd.Name)
				if methodGrp == nil {
					continue
				}

				ast.Inspect(fd, func(n ast.Node) bool {
					ident, ok := n.(*ast.Ident)
					if !ok {
						return true
					}
					ref := ix.Group(ident)
					if ref == nil || seen[ref] || !ref.IsPackageScope() {
						return true
					}
					// Skip the type itself and its methods.
					if ref == typeGrp || ref.Kind == mast.Method {
						return true
					}
					seen[ref] = true

					def := ref.DefIdent()
					if def == nil || def.File == nil {
						return true
					}

					*deps = append(*deps, depsEntry{
						Name: ref.Name,
						Kind: objectKindString(ref.Kind),
						File: relPath(absDir, def.File.Path),
						Line: ix.Fset.Position(def.Ident.Pos()).Line,
					})
					return true
				})
			}
		}
	}
}

// findDeclNode finds the top-level declaration node containing ident.
func findDeclNode(file *ast.File, ident *ast.Ident) ast.Node {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == ident {
				return d
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name == ident {
						if len(d.Specs) == 1 {
							return d
						}
						return s
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name == ident {
							if len(d.Specs) == 1 {
								return d
							}
							return s
						}
					}
				}
			}
		}
	}
	return nil
}
