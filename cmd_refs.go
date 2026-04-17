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

// cmdRefs shows where declarations are referenced.

type cmdRefs struct {
	jsonOutput bool
	args       []string
}

func (c *cmdRefs) Setup(params clingy.Parameters) {
	c.jsonOutput = params.Flag("json", "emit JSON output", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.args = params.Arg("specifier", "declaration specifier (e.g. Server, ./pkg.Name, file.go:Name)",
		clingy.Repeated).([]string)
}

func (c *cmdRefs) Execute(ctx context.Context) error {
	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	var results []refsResult
	for _, arg := range c.args {
		r, err := resolveRefs(ix, absDir, arg)
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
		for _, ref := range r.Refs {
			fmt.Fprintf(w, "  %s:%d\n", ref.File, ref.Line)
		}
		if len(r.Refs) == 0 {
			fmt.Fprintln(w, "  (no references)")
		}
	}
	return nil
}

type refsResult struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	DefFile string    `json:"def_file"`
	DefLine int       `json:"def_line"`
	Refs    []refsLoc `json:"refs"`
}

type refsLoc struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

func resolveRefs(ix *mast.Index, absDir string, arg string) (refsResult, error) {
	item, err := rules.ParseItem(arg)
	if err != nil {
		return refsResult{}, fmt.Errorf("parsing %q: %w", arg, err)
	}

	source := relo.ResolveSource(ix, item.Source, absDir)

	var id *ast.Ident
	var kindStr string

	switch {
	case item.Field != "":
		id = ix.FindFieldDef(item.Name, item.Field, source)
		if id == nil {
			return refsResult{}, fmt.Errorf("could not find field/method %q on %q", item.Field, item.Name)
		}
		kindStr = "field"
	default:
		id = ix.FindDef(item.Name, source)
		if id == nil {
			src := ""
			if item.Source != "" {
				src = " in " + item.Source
			}
			return refsResult{}, fmt.Errorf("could not find %q%s", item.Name, src)
		}
	}

	grp := ix.Group(id)
	if grp == nil {
		return refsResult{}, fmt.Errorf("no group for %q", arg)
	}

	if kindStr == "" {
		kindStr = objectKindString(grp.Kind)
	}

	// Find the definition location.
	var defFile string
	var defLine int
	if def := grp.DefIdent(); def != nil && def.File != nil {
		defFile = relPath(absDir, def.File.Path)
		defLine = ix.Fset.Position(def.Ident.Pos()).Line
	}

	name := grp.Name
	if item.Field != "" {
		name = item.Name + "#" + item.Field
	}

	// Collect use locations.
	var refs []refsLoc
	for _, ident := range grp.Idents {
		if ident.Kind != mast.Use || ident.File == nil {
			continue
		}
		refs = append(refs, refsLoc{
			File: relPath(absDir, ident.File.Path),
			Line: ix.Fset.Position(ident.Ident.Pos()).Line,
		})
	}

	sort.Slice(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		return refs[i].Line < refs[j].Line
	})

	return refsResult{
		Name:    name,
		Kind:    kindStr,
		DefFile: defFile,
		DefLine: defLine,
		Refs:    refs,
	}, nil
}

func objectKindString(k mast.ObjectKind) string {
	switch k {
	case mast.TypeName:
		return "type"
	case mast.Func:
		return "func"
	case mast.Method:
		return "method"
	case mast.Field:
		return "field"
	case mast.Var:
		return "var"
	case mast.Const:
		return "const"
	default:
		return "unknown"
	}
}

func relPath(absDir, path string) string {
	rel, err := filepath.Rel(absDir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
