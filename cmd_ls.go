package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"

	"github.com/zeebo/clingy"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

// cmdLs lists package-level declarations in the codebase.

type cmdLs struct {
	jsonOutput bool
	args       []string
}

func (c *cmdLs) Setup(params clingy.Parameters) {
	c.jsonOutput = params.Flag("json", "emit JSON output", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.args = params.Arg("specifier", "declaration specifier (e.g. Server, ./pkg.Name, file.go:Name)",
		clingy.Repeated, clingy.Optional).([]string)
}

func (c *cmdLs) Execute(ctx context.Context) error {
	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	var files []lsFile
	if len(c.args) == 0 {
		files = collectDecls(ix, absDir, nil)
	} else {
		filter, err := buildFilter(ix, absDir, c.args)
		if err != nil {
			return err
		}
		files = collectDecls(ix, absDir, filter)
	}

	w := clingy.Stdout(ctx)
	if c.jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(files)
	}

	for i, f := range files {
		if i > 0 {
			fmt.Fprintln(w)
		}
		header := f.File + "  (package " + f.Package
		if f.BuildTag != "" {
			header += ", " + f.BuildTag
		}
		header += ")"
		fmt.Fprintln(w, header)

		for _, d := range f.Decls {
			name := d.Name
			if d.Receiver != "" {
				name = d.Receiver + "." + name
			}
			lines := "lines"
			if d.Lines == 1 {
				lines = "line"
			}
			fmt.Fprintf(w, "  %-7s %-30s %d:%d\t%d %s\n",
				d.Kind, name, d.Line, d.EndLine, d.Lines, lines)
		}
	}
	return nil
}

// lsFilter tracks which declarations to include based on specifier arguments.
type lsFilter struct {
	names map[string]bool // declaration names to show (including their methods)
}

func (f *lsFilter) match(name, kind, receiver string) bool {
	if f == nil {
		return true
	}
	if kind == "method" {
		return f.names[receiver]
	}
	return f.names[name]
}

// buildFilter parses specifier arguments and resolves them against the index
// to find matching declaration names.
func buildFilter(ix *mast.Index, absDir string, args []string) (*lsFilter, error) {
	f := &lsFilter{names: make(map[string]bool)}
	for _, arg := range args {
		item, err := rules.ParseItem(arg)
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", arg, err)
		}
		source := relo.ResolveSource(ix, item.Source, absDir)
		id := ix.FindDef(item.Name, source)
		if id == nil {
			src := ""
			if item.Source != "" {
				src = " in " + item.Source
			}
			return nil, fmt.Errorf("could not find %q%s", item.Name, src)
		}
		f.names[item.Name] = true
	}
	return f, nil
}

type lsFile struct {
	File     string    `json:"file"`
	Package  string    `json:"package"`
	BuildTag string    `json:"build_tag,omitempty"`
	Decls    []lsEntry `json:"decls"`
}

type lsEntry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Receiver string `json:"receiver,omitempty"`
	Line     int    `json:"line"`
	EndLine  int    `json:"end"`
	Lines    int    `json:"lines"`
}

func collectDecls(ix *mast.Index, absDir string, filter *lsFilter) []lsFile {
	var files []lsFile
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			rel, err := filepath.Rel(absDir, file.Path)
			if err != nil {
				rel = file.Path
			}
			rel = filepath.ToSlash(rel)

			var decls []lsEntry
			for _, decl := range file.Syntax.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					entry := lsEntry{
						Name: d.Name.Name,
						Kind: "func",
					}
					if d.Recv != nil {
						entry.Kind = "method"
						entry.Receiver = mast.ReceiverTypeName(d.Recv)
					}
					if !filter.match(entry.Name, entry.Kind, entry.Receiver) {
						continue
					}
					entry.Line, entry.EndLine = declLines(ix.Fset, d, d.Doc)
					entry.Lines = entry.EndLine - entry.Line + 1
					decls = append(decls, entry)

				case *ast.GenDecl:
					if d.Tok == token.IMPORT {
						continue
					}
					kind := genDeclKind(d.Tok)
					singleSpec := len(d.Specs) == 1
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if !filter.match(s.Name.Name, kind, "") {
								continue
							}
							entry := lsEntry{Name: s.Name.Name, Kind: kind}
							if singleSpec {
								entry.Line, entry.EndLine = declLines(ix.Fset, d, d.Doc)
							} else {
								entry.Line, entry.EndLine = declLines(ix.Fset, s, s.Doc)
							}
							entry.Lines = entry.EndLine - entry.Line + 1
							decls = append(decls, entry)
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if !filter.match(name.Name, kind, "") {
									continue
								}
								entry := lsEntry{Name: name.Name, Kind: kind}
								if singleSpec {
									entry.Line, entry.EndLine = declLines(ix.Fset, d, d.Doc)
								} else {
									entry.Line, entry.EndLine = declLines(ix.Fset, s, s.Doc)
								}
								entry.Lines = entry.EndLine - entry.Line + 1
								decls = append(decls, entry)
							}
						}
					}
				}
			}

			if len(decls) == 0 {
				continue
			}
			f := lsFile{
				File:    rel,
				Package: pkg.Name,
				Decls:   decls,
			}
			if file.BuildTag != "" {
				f.BuildTag = file.BuildTag
			}
			files = append(files, f)
		}
	}
	return files
}

func declLines(fset *token.FileSet, node ast.Node, doc *ast.CommentGroup) (start, end int) {
	start = fset.Position(node.Pos()).Line
	if doc != nil {
		if docLine := fset.Position(doc.Pos()).Line; docLine < start {
			start = docLine
		}
	}
	end = fset.Position(node.End()).Line
	return start, end
}

func genDeclKind(tok token.Token) string {
	switch tok {
	case token.TYPE:
		return "type"
	case token.VAR:
		return "var"
	case token.CONST:
		return "const"
	default:
		return "unknown"
	}
}
