package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zeebo/clingy"

	"github.com/loov/gorelo/cluster"
	"github.com/loov/gorelo/mast"
)

// cmdCluster suggests declaration groupings using community detection.

type cmdCluster struct {
	jsonOutput bool
	rulesOut   bool
	gamma      float64
	args       []string
}

func (c *cmdCluster) Setup(params clingy.Parameters) {
	c.jsonOutput = params.Flag("json", "emit JSON output", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.rulesOut = params.Flag("rules", "output gorelo move rules", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.gamma = params.Flag("gamma", "resolution parameter (higher = more clusters)", float64(1.0),
		clingy.Transform(func(s string) (float64, error) { return strconv.ParseFloat(s, 64) }),
		clingy.Short('g')).(float64)
	c.args = params.Arg("specifier", "package path filter (e.g. ./relo, default: all packages)",
		clingy.Repeated, clingy.Optional).([]string)
}

func (c *cmdCluster) Execute(ctx context.Context) error {
	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	// Determine which packages to analyze.
	pkgs := filterPackages(ix, absDir, c.args)
	if len(pkgs) == 0 {
		fmt.Fprintln(clingy.Stdout(ctx), "No packages found.")
		return nil
	}

	w := clingy.Stdout(ctx)
	first := true
	for _, pkg := range pkgs {
		g, nodeGroups := buildGraph(ix, absDir, pkg)
		if g.NumNodes() < 2 {
			continue
		}

		communities := cluster.Leiden(g, c.gamma, 0)
		modularity := cluster.Modularity(g, communities, c.gamma)
		stats := cluster.CommunityStats(g, communities)

		sort.Slice(stats, func(i, j int) bool {
			return stats[i].Size > stats[j].Size
		})

		// Check if there's a meaningful split (at least 2 clusters with 2+ nodes).
		bigClusters := 0
		for _, st := range stats {
			if st.Size >= 2 {
				bigClusters++
			}
		}
		multiCluster := bigClusters >= 2

		if c.jsonOutput {
			if !first {
				fmt.Fprintln(w)
			}
			first = false
			if err := c.writeJSON(w, pkg, g, stats, modularity); err != nil {
				return err
			}
			continue
		}

		if c.rulesOut {
			if !multiCluster {
				continue
			}
			if !first {
				fmt.Fprintln(w)
			}
			first = false
			if err := c.writeRules(w, pkg, g, stats); err != nil {
				return err
			}
			continue
		}

		if !first {
			fmt.Fprintln(w)
		}
		first = false

		if !multiCluster {
			pkgLabel := relPath(absDir, packageDir(pkg))
			fmt.Fprintf(w, "package %s  (%d declarations) — no split found at gamma=%.1f, try higher gamma\n",
				pkgLabel, g.NumNodes(), c.gamma)
			continue
		}

		c.writeText(w, ix, pkg, g, nodeGroups, stats, modularity, absDir)
	}

	return nil
}

func (c *cmdCluster) writeText(w interface{ Write([]byte) (int, error) }, ix *mast.Index, pkg *mast.Package, g *cluster.Graph, nodeGroups map[int]*mast.Group, stats []cluster.CommunityStat, modularity float64, absDir string) {
	totalEdges := 0
	for _, adj := range g.Adj {
		totalEdges += len(adj)
	}

	pkgLabel := relPath(absDir, packageDir(pkg))
	fmt.Fprintf(w, "package %s  (%d declarations, %d dependencies)\n\n",
		pkgLabel, g.NumNodes(), totalEdges/2)

	clusterNum := 0
	for _, st := range stats {
		if st.Size < 2 {
			continue
		}
		clusterNum++
		fmt.Fprintf(w, "  Cluster %d  (cohesion: %.2f)\n", clusterNum, st.Cohesion)

		entries := sortedEntries(g, st.Nodes)
		for _, e := range entries {
			fmt.Fprintf(w, "    %-7s %-30s %s:%d\n", e.kind, e.label, e.file, e.line)
		}

		// Show methods under their type.
		for _, nodeID := range st.Nodes {
			grp := nodeGroups[nodeID]
			if grp == nil || grp.Kind != mast.ObjectTypeName {
				continue
			}
			methods := collectMethods(ix, grp, absDir)
			for _, m := range methods {
				fmt.Fprintf(w, "    %-7s   .%-28s %s:%d\n", "method", m.name, m.file, m.line)
			}
		}
		fmt.Fprintln(w)
	}

	// Show unclustered (singleton) declarations.
	var singletons []nodeEntry
	for _, st := range stats {
		if st.Size >= 2 {
			continue
		}
		for _, nodeID := range st.Nodes {
			n := g.Nodes[nodeID]
			singletons = append(singletons, nodeEntry{
				label: n.Label, kind: n.Kind, file: n.File, line: n.Line,
			})
		}
	}

	if len(singletons) > 0 {
		fmt.Fprintf(w, "  Unclustered (%d)\n", len(singletons))
		for _, e := range singletons {
			fmt.Fprintf(w, "    %-7s %-30s %s:%d\n", e.kind, e.label, e.file, e.line)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "  Modularity: %.2f\n", modularity)
}

// filterPackages selects packages to analyze based on args.
// With no args, returns all packages. With args, matches package paths
// that start with the given prefixes.
func filterPackages(ix *mast.Index, absDir string, args []string) []*mast.Package {
	if len(args) == 0 {
		return ix.Pkgs
	}

	// Resolve relative args to absolute paths for matching.
	var prefixes []string
	for _, arg := range args {
		arg = strings.TrimSuffix(arg, "/...")
		abs := arg
		if !filepath.IsAbs(arg) {
			abs = filepath.Join(absDir, arg)
		}
		prefixes = append(prefixes, abs)
	}

	var result []*mast.Package
	for _, pkg := range ix.Pkgs {
		dir := packageDir(pkg)
		for _, prefix := range prefixes {
			if dir == prefix || strings.HasPrefix(dir, prefix+string(filepath.Separator)) {
				result = append(result, pkg)
				break
			}
		}
	}
	return result
}

func packageDir(pkg *mast.Package) string {
	if len(pkg.Files) == 0 {
		return ""
	}
	return filepath.Dir(pkg.Files[0].Path)
}

type nodeEntry struct {
	label string
	kind  string
	file  string
	line  int
}

func sortedEntries(g *cluster.Graph, nodeIDs []int) []nodeEntry {
	entries := make([]nodeEntry, len(nodeIDs))
	for i, id := range nodeIDs {
		n := g.Nodes[id]
		entries[i] = nodeEntry{label: n.Label, kind: n.Kind, file: n.File, line: n.Line}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].file != entries[j].file {
			return entries[i].file < entries[j].file
		}
		return entries[i].line < entries[j].line
	})
	return entries
}

type methodInfo struct {
	name string
	file string
	line int
}

func collectMethods(ix *mast.Index, typeGrp *mast.Group, absDir string) []methodInfo {
	var methods []methodInfo
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
				methodGrp := ix.Group(fd.Name)
				if methodGrp == nil || methodGrp.Pkg != typeGrp.Pkg {
					continue
				}
				methods = append(methods, methodInfo{
					name: fd.Name.Name,
					file: relPath(absDir, file.Path),
					line: ix.Fset.Position(fd.Name.Pos()).Line,
				})
			}
		}
	}
	sort.Slice(methods, func(i, j int) bool {
		return methods[i].line < methods[j].line
	})
	return methods
}

// buildGraph creates a weighted intra-package dependency graph.
// Only declarations within pkg become nodes. References to other packages
// are ignored, keeping the analysis focused on internal structure.
func buildGraph(ix *mast.Index, absDir string, pkg *mast.Package) (*cluster.Graph, map[int]*mast.Group) {
	type nodeInfo struct {
		id    int
		group *mast.Group
		label string
		kind  string
		file  string
		line  int
		// For types, collect AST decl nodes for methods too.
		declNodes []ast.Node
	}

	groupToNode := make(map[*mast.Group]int)
	var nodes []nodeInfo

	// First pass: create nodes for types, funcs, vars, consts in this package.
	for _, file := range pkg.Files {
		if isTestFile(file.Path) {
			continue
		}

		for _, decl := range file.Syntax.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil {
					continue // methods handled below
				}
				if d.Name.Name == "init" {
					continue
				}

				grp := ix.Group(d.Name)
				if grp == nil || !grp.IsPackageScope() {
					continue
				}
				if _, ok := groupToNode[grp]; ok {
					continue
				}

				id := len(nodes)
				groupToNode[grp] = id
				nodes = append(nodes, nodeInfo{
					id:        id,
					group:     grp,
					label:     d.Name.Name,
					kind:      "func",
					file:      relPath(absDir, file.Path),
					line:      ix.Fset.Position(d.Name.Pos()).Line,
					declNodes: []ast.Node{d},
				})

			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						grp := ix.Group(s.Name)
						if grp == nil || !grp.IsPackageScope() {
							continue
						}
						if _, ok := groupToNode[grp]; ok {
							continue
						}

						id := len(nodes)
						groupToNode[grp] = id
						declNode := findDeclNode(file.Syntax, s.Name)
						if declNode == nil {
							continue
						}
						nodes = append(nodes, nodeInfo{
							id:        id,
							group:     grp,
							label:     s.Name.Name,
							kind:      "type",
							file:      relPath(absDir, file.Path),
							line:      ix.Fset.Position(s.Name.Pos()).Line,
							declNodes: []ast.Node{declNode},
						})

					case *ast.ValueSpec:
						for _, name := range s.Names {
							grp := ix.Group(name)
							if grp == nil || !grp.IsPackageScope() {
								continue
							}
							if _, ok := groupToNode[grp]; ok {
								continue
							}

							id := len(nodes)
							groupToNode[grp] = id
							declNode := findDeclNode(file.Syntax, name)
							if declNode == nil {
								continue
							}
							nodes = append(nodes, nodeInfo{
								id:        id,
								group:     grp,
								label:     name.Name,
								kind:      genDeclKind(d.Tok),
								file:      relPath(absDir, file.Path),
								line:      ix.Fset.Position(name.Pos()).Line,
								declNodes: []ast.Node{declNode},
							})
						}
					}
				}
			}
		}
	}

	// Second pass: handle methods.
	// Same-file methods merge into their receiver type's node (they're part
	// of the same concern). Cross-file methods become separate nodes with
	// a weighted edge to their type — this lets the algorithm detect when
	// a method like Plan.Apply belongs to a different concern than Plan itself.
	type deferredEdge struct{ from, to int }
	var methodEdges []deferredEdge

	for _, file := range pkg.Files {
		if isTestFile(file.Path) {
			continue
		}
		for _, decl := range file.Syntax.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil {
				continue
			}
			methodGrp := ix.Group(fd.Name)
			if methodGrp == nil {
				continue
			}

			recvName := mast.ReceiverTypeName(fd.Recv)
			for _, ni := range nodes {
				if ni.kind != "type" || ni.label != recvName || ni.group.Pkg != methodGrp.Pkg {
					continue
				}
				sameFile := relPath(absDir, file.Path) == ni.file
				if sameFile {
					// Same file: merge into the type's node.
					groupToNode[methodGrp] = ni.id
					nodes[ni.id].declNodes = append(nodes[ni.id].declNodes, fd)
				} else {
					// Cross-file: create a separate node, link later.
					id := len(nodes)
					groupToNode[methodGrp] = id
					nodes = append(nodes, nodeInfo{
						id:        id,
						group:     methodGrp,
						label:     recvName + "." + fd.Name.Name,
						kind:      "method",
						file:      relPath(absDir, file.Path),
						line:      ix.Fset.Position(fd.Name.Pos()).Line,
						declNodes: []ast.Node{fd},
					})
					methodEdges = append(methodEdges, deferredEdge{from: id, to: ni.id})
				}
				break
			}
		}
	}

	// Build the graph.
	g := cluster.NewGraph(len(nodes))
	for _, ni := range nodes {
		g.Nodes[ni.id] = cluster.Node{
			ID:    ni.id,
			Label: ni.label,
			Kind:  ni.kind,
			File:  ni.file,
			Line:  ni.line,
		}
	}

	// Add strong edges between cross-file methods and their receiver type.
	for _, me := range methodEdges {
		g.AddEdge(me.from, me.to, 2)
	}

	// Build edges by walking AST. Only intra-package references
	// (those that map to a node in groupToNode) become edges.
	// Each reference contributes weight 1, so heavily-used connections
	// are stronger than one-time references.
	for _, ni := range nodes {
		for _, declNode := range ni.declNodes {
			ast.Inspect(declNode, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				ref := ix.Group(ident)
				if ref == nil || !ref.IsPackageScope() {
					return true
				}
				targetNode, ok := groupToNode[ref]
				if !ok || targetNode == ni.id {
					return true
				}
				g.AddEdge(ni.id, targetNode, 1)
				return true
			})
		}
	}

	nodeGroups := make(map[int]*mast.Group, len(nodes))
	for _, ni := range nodes {
		nodeGroups[ni.id] = ni.group
	}

	return g, nodeGroups
}

func isTestFile(path string) bool {
	base := filepath.Base(path)
	return len(base) > len("_test.go") && base[len(base)-len("_test.go"):] == "_test.go"
}

// writeJSON emits structured JSON output.
func (c *cmdCluster) writeJSON(w interface{ Write([]byte) (int, error) }, pkg *mast.Package, g *cluster.Graph, stats []cluster.CommunityStat, modularity float64) error {
	type jsonDecl struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		File string `json:"file"`
		Line int    `json:"line"`
	}
	type jsonCluster struct {
		ID       int        `json:"id"`
		Cohesion float64    `json:"cohesion"`
		Decls    []jsonDecl `json:"decls"`
	}
	type jsonResult struct {
		Package           string        `json:"package"`
		TotalDeclarations int           `json:"total_declarations"`
		TotalDependencies int           `json:"total_dependencies"`
		Modularity        float64       `json:"modularity"`
		Gamma             float64       `json:"gamma"`
		Clusters          []jsonCluster `json:"clusters"`
	}

	totalEdges := 0
	for _, adj := range g.Adj {
		totalEdges += len(adj)
	}

	result := jsonResult{
		Package:           pkg.Path,
		TotalDeclarations: g.NumNodes(),
		TotalDependencies: totalEdges / 2,
		Modularity:        modularity,
		Gamma:             c.gamma,
	}

	for i, st := range stats {
		jc := jsonCluster{
			ID:       i + 1,
			Cohesion: st.Cohesion,
		}
		entries := sortedEntries(g, st.Nodes)
		for _, e := range entries {
			jc.Decls = append(jc.Decls, jsonDecl{
				Name: e.label,
				Kind: e.kind,
				File: e.file,
				Line: e.line,
			})
		}
		result.Clusters = append(result.Clusters, jc)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// writeRules emits gorelo move rule syntax.
func (c *cmdCluster) writeRules(w interface{ Write([]byte) (int, error) }, _ *mast.Package, g *cluster.Graph, stats []cluster.CommunityStat) error {
	clusterNum := 0
	for _, st := range stats {
		if st.Size < 2 {
			continue
		}
		clusterNum++

		entries := sortedEntries(g, st.Nodes)

		// Use the first file as the target.
		targetFile := entries[0].file

		fmt.Fprintf(w, "# Cluster %d (cohesion: %.2f)\n", clusterNum, st.Cohesion)
		fmt.Fprintf(w, "%s <-\n", targetFile)
		for _, e := range entries {
			fmt.Fprintf(w, "    %s\n", e.label)
		}
		fmt.Fprintln(w)
	}
	return nil
}
