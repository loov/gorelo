package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"html"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/loov/gorelo/mast"
)

//go:embed index.html file.html
var templates embed.FS

//go:embed style.css vendor/open-props/*.min.css
var styleFS embed.FS

var styleCSS = func() []byte {
	// Concatenate open-props modules with our styles into a single CSS blob.
	modules := []string{
		"vendor/open-props/palette.min.css",
		"vendor/open-props/sizes.min.css",
		"vendor/open-props/fonts.min.css",
		"vendor/open-props/borders.min.css",
		"vendor/open-props/shadows.min.css",
		"vendor/open-props/easings.min.css",
		"vendor/open-props/durations.min.css",
		"style.css",
	}
	var buf []byte
	for _, name := range modules {
		data, err := styleFS.ReadFile(name)
		if err != nil {
			panic(err)
		}
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	return buf
}()

//go:embed file.js
var fileJS []byte

//go:embed theme.js
var themeJS []byte

//go:embed vendor/jetbrains-mono/JetBrainsMono-Regular.woff2
var fontRegular []byte

//go:embed vendor/jetbrains-mono/JetBrainsMono-Bold.woff2
var fontBold []byte

var tmpl = template.Must(template.ParseFS(templates, "*.html"))

func main() {
	dir := flag.String("dir", ".", "directory to load")
	listen := flag.String("listen", "127.0.0.1:8080", "listen address")
	flag.Parse()

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("loading packages in %s ...", absDir)
	ix, err := mast.Load(&mast.Config{Dir: absDir}, "./...")
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range ix.Errors {
		log.Printf("warning: %v", e)
	}
	log.Printf("loaded %d packages", len(ix.Pkgs))

	s := &server{
		ix:       ix,
		dir:      absDir,
		groupIDs: map[*mast.Group]int{},
		groups:   map[int]*mast.Group{},
	}
	// Assign stable global IDs to every group.
	nextID := 1
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f.Syntax, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				g := ix.Group(id)
				if g == nil {
					return true
				}
				if _, exists := s.groupIDs[g]; !exists {
					s.groupIDs[g] = nextID
					s.groups[nextID] = g
					nextID++
				}
				return true
			})
		}
	}
	// Cache file lines for context snippets.
	s.fileLines = map[string][]string{}
	for _, pkg := range ix.Pkgs {
		for _, f := range pkg.Files {
			if _, ok := s.fileLines[f.Path]; ok {
				continue
			}
			data, err := os.ReadFile(f.Path)
			if err != nil {
				continue
			}
			s.fileLines[f.Path] = strings.Split(string(data), "\n")
		}
	}

	http.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(styleCSS)
	})
	http.HandleFunc("/file.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(fileJS)
	})
	http.HandleFunc("/theme.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(themeJS)
	})
	serveFont := func(data []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "font/woff2")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Write(data)
		}
	}
	http.HandleFunc("/fonts/JetBrainsMono-Regular.woff2", serveFont(fontRegular))
	http.HandleFunc("/fonts/JetBrainsMono-Bold.woff2", serveFont(fontBold))
	http.HandleFunc("/", s.handleIndex)
	http.HandleFunc("/file", s.handleFile)
	http.HandleFunc("/group", s.handleGroup)

	log.Printf("listening on http://%s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

type server struct {
	ix        *mast.Index
	dir       string
	groupIDs  map[*mast.Group]int
	groups    map[int]*mast.Group
	fileLines map[string][]string // path → lines
}

type indexPkg struct {
	Path  string
	Files []indexFile
}

type indexFile struct {
	Rel      string
	BuildTag string
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	var pkgs []indexPkg
	for _, pkg := range s.ix.Pkgs {
		p := indexPkg{Path: pkg.Path}
		for _, f := range pkg.Files {
			p.Files = append(p.Files, indexFile{
				Rel:      relativePath(s.dir, f.Path),
				BuildTag: f.BuildTag,
			})
		}
		pkgs = append(pkgs, p)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "index.html", struct{ Pkgs []indexPkg }{pkgs}); err != nil {
		log.Printf("index template: %v", err)
	}
}

type sourceLine struct {
	Num     int
	Content template.HTML
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Find the file in the index.
	var file *mast.File
	for _, pkg := range s.ix.Pkgs {
		for _, f := range pkg.Files {
			if relativePath(s.dir, f.Path) == relPath {
				file = f
				break
			}
		}
		if file != nil {
			break
		}
	}
	if file == nil {
		http.Error(w, "file not found in index", http.StatusNotFound)
		return
	}

	src, err := os.ReadFile(file.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Collect ident spans sorted by position.
	type identSpan struct {
		start     int // byte offset in src
		end       int
		group     int // global group ID (0 = untracked)
		kind      string
		qualifier bool // true for the "pkg." part of a qualified reference
	}

	// Collect qualifier idents: maps qualifier *ast.Ident → group ID of the qualified name.
	qualifierGroup := map[*ast.Ident]int{}
	// Map byte offset → *ast.Ident for resolving qualifier spans.
	identByOffset := map[int]*ast.Ident{}

	var spans []identSpan
	ast.Inspect(file.Syntax, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		pos := s.ix.Fset.Position(id.Pos())
		endPos := s.ix.Fset.Position(id.End())

		start := pos.Offset
		end := endPos.Offset
		if start < 0 || end > len(src) {
			return true
		}

		identByOffset[start] = id

		g := s.ix.Group(id)
		sp := identSpan{start: start, end: end}
		if g == nil {
			sp.kind = "untracked"
		} else {
			sp.group = s.groupIDs[g]
			sp.kind = "use"
			for _, ident := range g.Idents {
				if ident.Ident == id {
					if ident.Kind == mast.Def {
						sp.kind = "def"
					}
					if ident.Qualifier != nil {
						qualifierGroup[ident.Qualifier] = sp.group
					}
					break
				}
			}
		}
		spans = append(spans, sp)
		return true
	})

	// Mark qualifier spans and extend them to include the "." separator.
	for i := range spans {
		if id, ok := identByOffset[spans[i].start]; ok {
			if gid, ok := qualifierGroup[id]; ok {
				spans[i].qualifier = true
				spans[i].group = gid
				if spans[i].end < len(src) && src[spans[i].end] == '.' {
					spans[i].end++
				}
			}
		}
	}

	sort.Slice(spans, func(i, j int) bool {
		return spans[i].start < spans[j].start
	})

	// Build annotated source lines.
	srcStr := string(src)
	spanIdx := 0

	renderLine := func(lineContent string, lineOffset int) template.HTML {
		var sb strings.Builder
		cursor := lineOffset
		lineEnd := lineOffset + len(lineContent)
		for spanIdx < len(spans) && spans[spanIdx].start < lineEnd {
			sp := spans[spanIdx]
			if sp.end <= lineOffset {
				spanIdx++
				continue
			}
			if sp.start > cursor {
				sb.WriteString(html.EscapeString(srcStr[cursor:sp.start]))
			}
			end := min(sp.end, lineEnd)
			cls := "ident " + sp.kind
			if sp.qualifier {
				cls += " qualifier"
			}
			if sp.group > 0 {
				fmt.Fprintf(&sb, `<span class="%s" data-group="%d">`, cls, sp.group)
			} else {
				fmt.Fprintf(&sb, `<span class="%s">`, cls)
			}
			sb.WriteString(html.EscapeString(srcStr[sp.start:end]))
			sb.WriteString(`</span>`)
			cursor = end
			if sp.end <= lineEnd {
				spanIdx++
			} else {
				break
			}
		}
		if cursor < lineEnd {
			sb.WriteString(html.EscapeString(srcStr[cursor:lineEnd]))
		}
		return template.HTML(sb.String())
	}

	var lines []sourceLine
	lineNum := 1
	lineStart := 0
	for i, b := range src {
		if b == '\n' {
			lines = append(lines, sourceLine{
				Num:     lineNum,
				Content: renderLine(srcStr[lineStart:i], lineStart),
			})
			lineNum++
			lineStart = i + 1
		}
	}
	if lineStart < len(src) {
		lines = append(lines, sourceLine{
			Num:     lineNum,
			Content: renderLine(srcStr[lineStart:], lineStart),
		})
	}

	data := struct {
		Title string
		Lines []sourceLine
	}{
		Title: relPath,
		Lines: lines,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "file.html", data); err != nil {
		log.Printf("file template: %v", err)
	}
}

// groupHighlight marks a single referenced identifier within a snippet.
type groupHighlight struct {
	Line int    `json:"line"`
	Col  int    `json:"col"`  // 1-based column (byte offset in line)
	Len  int    `json:"len"`  // length of identifier in bytes
	Kind string `json:"kind"` // "def" or "use"
}

// groupSnippet is a merged context range, possibly covering multiple references.
type groupSnippet struct {
	ContextStart int              `json:"contextStart"` // 1-based
	Context      []string         `json:"context"`
	Highlights   []groupHighlight `json:"highlights"`
}

// groupFile collects all merged snippets for one file.
type groupFile struct {
	File    string         `json:"file"`
	Pkg     string         `json:"pkg"` // package path the file belongs to
	Snippets []groupSnippet `json:"snippets"`
}

// groupResponse is the JSON returned by /group.
type groupResponse struct {
	Name  string      `json:"name"`
	Kind  string      `json:"kind"`
	Pkg   string      `json:"pkg"`
	Files []groupFile `json:"files"`
}

func (s *server) handleGroup(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	g, ok := s.groups[id]
	if !ok {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}

	const contextRadius = 5

	// Collect idents grouped by file path.
	type identInfo struct {
		line int
		col  int // 1-based byte column
		len  int
		kind string
	}
	byFile := map[string][]identInfo{}
	filePkg := map[string]string{} // rel path → package path
	var fileOrder []string
	for _, ident := range g.Idents {
		pos := s.ix.Fset.Position(ident.Ident.Pos())
		rel := relativePath(s.dir, ident.File.Path)
		kind := "use"
		if ident.Kind == mast.Def {
			kind = "def"
		}
		if _, seen := byFile[rel]; !seen {
			fileOrder = append(fileOrder, rel)
			if ident.File.Pkg != nil {
				filePkg[rel] = ident.File.Pkg.Path
			}
		}
		byFile[rel] = append(byFile[rel], identInfo{
			line: pos.Line,
			col:  pos.Column,
			len:  len(ident.Ident.Name),
			kind: kind,
		})
	}

	resp := groupResponse{
		Name: g.Name,
		Kind: objectKindString(g.Kind),
		Pkg:  g.Pkg,
	}

	for _, rel := range fileOrder {
		idents := byFile[rel]
		sort.Slice(idents, func(i, j int) bool { return idents[i].line < idents[j].line })

		// Find file lines for context.
		var fileLines []string
		for _, pkg := range s.ix.Pkgs {
			for _, f := range pkg.Files {
				if relativePath(s.dir, f.Path) == rel {
					fileLines = s.fileLines[f.Path]
					break
				}
			}
			if fileLines != nil {
				break
			}
		}
		if fileLines == nil {
			continue
		}

		// Merge overlapping context ranges.
		gf := groupFile{File: rel, Pkg: filePkg[rel]}
		var cur *groupSnippet
		curEnd := 0 // exclusive 0-based end of current snippet

		for _, id := range idents {
			start := max(id.line-1-contextRadius, 0)
			end := min(id.line-1+contextRadius+1, len(fileLines))

			if cur != nil && start <= curEnd {
				// Overlaps — extend current snippet.
				if end > curEnd {
					cur.Context = append(cur.Context, fileLines[curEnd:end]...)
					curEnd = end
				}
			} else {
				// Start new snippet.
				if cur != nil {
					gf.Snippets = append(gf.Snippets, *cur)
				}
				cur = &groupSnippet{
					ContextStart: start + 1,
					Context:      append([]string{}, fileLines[start:end]...),
				}
				curEnd = end
			}
			cur.Highlights = append(cur.Highlights, groupHighlight{Line: id.line, Col: id.col, Len: id.len, Kind: id.kind})
		}
		if cur != nil {
			gf.Snippets = append(gf.Snippets, *cur)
		}

		resp.Files = append(resp.Files, gf)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func relativePath(base, full string) string {
	rel, err := filepath.Rel(base, full)
	if err != nil {
		return full
	}
	return rel
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
	case mast.PackageName:
		return "package"
	case mast.Label:
		return "label"
	default:
		return "unknown"
	}
}
