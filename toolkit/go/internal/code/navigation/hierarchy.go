package navigation

import (
	"context"
	"fmt"

	cg "github.com/compforge/codegraph"
)

type Direction string

const (
	Parents  Direction = "parents"
	Children Direction = "children"
	Both     Direction = "both"
)

type HierarchyRequest struct {
	Position
	Direction Direction
}

// Entries are in depth-first order. Relation retains its original child-to-parent orientation.
type HierarchyEntry struct {
	Type         cg.Node     `json:"type"`
	Relation     cg.Relation `json:"relation"`
	Depth        int         `json:"depth"`
	AlreadyShown bool        `json:"alreadyShown,omitempty"`
}

type HierarchyReport struct {
	Declaration
	Direction Direction        `json:"direction"`
	Parents   []HierarchyEntry `json:"parents,omitempty"`
	Children  []HierarchyEntry `json:"children,omitempty"`
}

func (d Direction) Validate() error {
	switch d {
	case "", Both, Parents, Children:
		return nil
	default:
		return fmt.Errorf("direction must be parents, children, or both")
	}
}

// +spec=Traverse ancestors and descendants independently from the selected type. Preserve secondary and cyclic edges without expanding a type twice in one direction.
func Hierarchy(ctx context.Context, req HierarchyRequest) (HierarchyReport, error) {
	if err := req.Direction.Validate(); err != nil {
		return HierarchyReport{}, err
	}
	if req.Direction == "" {
		req.Direction = Both
	}
	decl, graph, err := resolve(ctx, req.Position, "type", cg.Class, cg.Interface, cg.Struct, cg.Type, cg.TypeAlias, cg.Trait, cg.Record, cg.Enum, cg.Union)
	report := HierarchyReport{Declaration: decl, Direction: req.Direction}
	if err != nil {
		return report, err
	}
	// Index type edges once. Repeated adjacency queries scan every graph edge for every type.
	parents := map[string][]cg.Relation{}
	children := map[string][]cg.Relation{}
	for _, edge := range graph.Relations() {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if edge.Kind != cg.Extends && edge.Kind != cg.Implements {
			continue
		}
		if req.Direction != Children {
			parents[edge.Source] = append(parents[edge.Source], edge)
		}
		if req.Direction != Parents {
			children[edge.Target] = append(children[edge.Target], edge)
		}
	}
	if req.Direction != Children {
		report.Parents, err = walkTypes(ctx, graph, decl.Target.ID, parents, Parents)
		if err != nil {
			return report, err
		}
	}
	if req.Direction != Parents {
		report.Children, err = walkTypes(ctx, graph, decl.Target.ID, children, Children)
	}
	return report, err
}

func walkTypes(ctx context.Context, graph *cg.Graph, root string, adjacent map[string][]cg.Relation, direction Direction) ([]HierarchyEntry, error) {
	seen := map[string]bool{root: true}
	// Each stack frame holds the remaining edges at that depth.
	stack := [][]cg.Relation{adjacent[root]}
	var entries []HierarchyEntry
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return entries, err
		}
		last := len(stack) - 1
		edges := stack[last]
		if len(edges) == 0 {
			stack = stack[:last]
			continue
		}
		edge := edges[0]
		stack[last] = edges[1:]
		id := edge.Target
		if direction == Children {
			id = edge.Source
		}
		node, ok := graph.Node(id)
		if !ok {
			return entries, fmt.Errorf("missing type node %s for relation %s", id, edge.ID)
		}
		entries = append(entries, HierarchyEntry{Type: node, Relation: edge, Depth: len(stack), AlreadyShown: seen[id]})
		if !seen[id] {
			seen[id] = true
			stack = append(stack, adjacent[id])
		}
	}
	return entries, nil
}
