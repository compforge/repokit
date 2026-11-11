package deadcode

import (
	"context"
	shared "github.com/compforge/codegraph"
	"slices"
)

// +spec=`Deadcode candidates have no incoming non-structural relation, regardless of confidence`
// This is an observation about the captured graph, not proof of safe deletion.
func unreferencedDeclarations(ctx context.Context, graph *Graph) ([]shared.Node, error) {
	declared, used := map[string]bool{}, map[string]bool{}
	for _, edge := range graph.Relations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch edge.Kind {
		case shared.Declares:
			declared[edge.Target] = true
		case shared.Contains, shared.Encloses, shared.InNamespace, shared.InDirectory, shared.OccursIn:
			// Ownership and source-location edges are not uses.
		default:
			used[edge.Target] = true
		}
	}
	nodes := []shared.Node{}
	for _, node := range graph.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !declared[node.ID] || used[node.ID] {
			continue
		}
		switch node.Kind {
		case shared.DocumentNodeKind, shared.DirectoryNodeKind, shared.Module, shared.Package, shared.Namespace:
			continue
		}
		nodes = append(nodes, node)
	}
	slices.SortFunc(nodes, compareNodes)
	return nodes, nil
}
