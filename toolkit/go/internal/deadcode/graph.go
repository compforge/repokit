package deadcode

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/impact"
)

type graphIndex struct {
	incoming   map[string][]int
	graph      *Graph
	nodes      map[string]shared.Node
	declared   map[string]bool
	parent     map[string]string
	owner      map[string]string
	next       map[string][]string
	candidates []Candidate
	roots      []Root
}

func inspect(ctx context.Context, graph *Graph, req Options) (*graphIndex, error) {
	d := &graphIndex{incoming: map[string][]int{}, graph: graph, nodes: map[string]shared.Node{}, declared: map[string]bool{}, parent: map[string]string{}, owner: map[string]string{}, next: map[string][]string{}, candidates: []Candidate{}, roots: []Root{}}
	for _, node := range graph.Nodes {
		d.nodes[node.ID] = node
	}
	for index, edge := range graph.Relations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d.incoming[edge.Target] = append(d.incoming[edge.Target], index)
		switch edge.Kind {
		case shared.Declares:
			d.declared[edge.Target] = true
		case shared.Encloses:
			d.parent[edge.Target] = edge.Source
		case shared.OccursIn:
			d.owner[edge.Source] = edge.Target
			// Source-use nodes are dependencies of their owner, not independent roots.
			d.next[edge.Target] = append(d.next[edge.Target], edge.Source)
		case shared.Contains:
			switch d.nodes[edge.Source].Kind {
			case shared.Class, shared.Struct, shared.Interface, shared.Type, shared.TypeAlias, shared.Enum:
				// A live type conservatively retains members, including dynamic dispatch
				// and serialization hooks that the graph cannot prove unused.
				d.next[edge.Source] = append(d.next[edge.Source], edge.Target)
			}
		case shared.InNamespace, shared.InDirectory:
		default:
			d.next[edge.Source] = append(d.next[edge.Source], edge.Target)
		}
	}
	var selected []shared.Node
	reasons := map[string]string{}
	if req.Mode == "unreachable" {
		production, tests, err := d.reachabilityRoots(req.Roots)
		if err != nil {
			return nil, err
		}
		live, err := walk(ctx, d.next, production)
		if err != nil {
			return nil, err
		}
		testLive, err := walk(ctx, d.next, tests)
		if err != nil {
			return nil, err
		}
		for _, node := range graph.Nodes {
			if !d.declared[node.ID] || !declaration(node) || live[node.ID] {
				continue
			}
			reasons[node.ID] = "unreachable"
			if testLive[node.ID] {
				reasons[node.ID] = "test_only"
			}
			selected = append(selected, node)
		}
	} else {
		var err error
		selected, err = unreferencedDeclarations(ctx, graph)
		if err != nil {
			return nil, err
		}
		for _, node := range selected {
			reasons[node.ID] = "unreferenced"
		}
	}
	for _, node := range selected {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		risks := []string{}
		if !graph.CaptureComplete {
			risks = append(risks, "incomplete_capture")
		}
		// A missing caller may be in another file: localizing all risk to the
		// candidate's own diagnostic locations would incorrectly imply safety.
		if len(graph.Diagnostics) > 0 {
			risks = append(risks, "graph_gaps")
		}
		if node.Entrypoint {
			risks = append(risks, "runtime_entrypoint")
		}
		switch node.Kind {
		case shared.Variable, shared.Field, shared.Property:
			risks = append(risks, "initialization_effects")
		}
		switch node.Kind {
		case shared.Method, shared.Field, shared.Property, shared.Constructor:
			risks = append(risks, "dynamic_dispatch")
		}
		if parent, ok := d.nodes[d.parent[node.ID]]; ok && parent.Kind == shared.DocumentNodeKind && !node.Entrypoint {
			risks = append(risks, "external_callers")
		}
		d.candidates = append(d.candidates, Candidate{Node: node, Reason: reasons[node.ID], Risks: risks, LostUses: []Use{}})
	}
	slices.SortFunc(d.candidates, func(a, b Candidate) int { return compareNodes(a.Node, b.Node) })
	slices.SortFunc(d.roots, func(a, b Root) int { return compareNodes(a.Node, b.Node) })
	return d, nil
}

func declaration(node shared.Node) bool {
	switch node.Kind {
	case shared.DocumentNodeKind, shared.DirectoryNodeKind, shared.Module, shared.Package, shared.Namespace:
		return false
	}
	return true
}

func compareNodes(a, b shared.Node) int {
	var left, right shared.Location
	if a.Location != nil {
		left = *a.Location
	}
	if b.Location != nil {
		right = *b.Location
	}
	if c := cmp.Compare(left.Path, right.Path); c != 0 {
		return c
	}
	if c := cmp.Compare(left.StartByte, right.StartByte); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// +spec=`Reachability is conditional on declared roots; no inferred application boundary means an error`
func (d *graphIndex) reachabilityRoots(requested []string) (production, tests []string, err error) {
	roots := map[string]string{}
	boundaries := 0
	for _, node := range d.graph.Nodes {
		if node.Entrypoint || node.Kind == shared.Export {
			reason := "entrypoint"
			if node.Kind == shared.Export {
				reason = "export"
			}
			roots[node.ID] = reason
			if !testNode(node) {
				boundaries++
			}
		}
	}
	for _, name := range requested {
		var matches []shared.Node
		if node, ok := d.nodes[name]; ok && d.declared[node.ID] {
			matches = []shared.Node{node}
		} else {
			for _, node := range d.graph.Nodes {
				if d.declared[node.ID] && node.QualifiedName == name {
					matches = append(matches, node)
				}
			}
		}
		if len(matches) != 1 {
			return nil, nil, fmt.Errorf("root %q matches %d declarations; use an exact node ID or unique qualified name", name, len(matches))
		}
		roots[matches[0].ID] = "explicit"
		boundaries++
	}
	if boundaries == 0 {
		return nil, nil, fmt.Errorf("unreachable mode requires recognized entrypoints/exports or explicit roots; no application or public API boundary was found")
	}
	for _, node := range d.graph.Nodes {
		if node.Kind == shared.DocumentNodeKind {
			roots[node.ID] = "module_initialization"
		}
		if node.Kind == shared.Variable && d.nodes[d.parent[node.ID]].Kind == shared.DocumentNodeKind {
			roots[node.ID] = "global_initialization"
		}
		if testNode(node) && d.declared[node.ID] && roots[node.ID] == "" {
			roots[node.ID] = "test"
		}
	}
	for _, node := range d.graph.Nodes {
		reason, ok := roots[node.ID]
		if !ok {
			continue
		}
		d.roots = append(d.roots, Root{Node: node, Reason: reason})
		if testNode(node) && reason != "explicit" {
			tests = append(tests, node.ID)
		} else {
			production = append(production, node.ID)
		}
	}
	return
}

func testNode(node shared.Node) bool {
	path := node.Path
	if node.Location != nil {
		path = node.Location.Path
	}
	return impact.IsTest(path)
}

func walk(ctx context.Context, next map[string][]string, seeds []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(seeds))
	queue := make([]string, 0, len(seeds))
	for _, id := range seeds {
		if !seen[id] {
			seen[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := queue[0]
		queue = queue[1:]
		for _, target := range next[id] {
			if !seen[target] {
				seen[target] = true
				queue = append(queue, target)
			}
		}
	}
	return seen, nil
}
