package analysis

import (
	"context"
	"slices"
	"testing"

	shared "github.com/compforge/codegraph"
)

func TestDeadcodeDiffAmbiguousAndIncompleteIdentity(t *testing.T) {
	node := func(id string) shared.Node {
		return shared.Node{ID: id, Kind: shared.Function, Name: "f", QualifiedName: "p.f", Language: "go", Location: &shared.Location{Path: "changed.go"}}
	}
	inspect := func(nodes []shared.Node, diagnostics []shared.Diagnostic) *deadcodeGraph {
		t.Helper()
		g := &GraphSnapshot{Nodes: nodes, Diagnostics: diagnostics, Snapshot: SnapshotReport{Complete: true}}
		for _, n := range nodes {
			g.Relations = append(g.Relations, shared.Relation{Source: "doc", Target: n.ID, Kind: shared.Declares})
		}
		d, err := inspectDeadcode(context.Background(), g, DeadcodeRequest{Mode: "unreferenced"})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	after := inspect([]shared.Node{node("after")}, nil)
	for _, tc := range []struct {
		name   string
		before *deadcodeGraph
		risk   string
	}{
		{"duplicate", inspect([]shared.Node{node("old-1"), node("old-2")}, nil), "ambiguous_identity"},
		{"missing baseline declarations", inspect(nil, []shared.Diagnostic{{Code: "parse_error"}}), "baseline_gaps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := changedDeadcode(context.Background(), tc.before, after, []string{"changed.go"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Change != "unmatched" || !slices.Contains(got[0].Risks, tc.risk) {
				t.Fatalf("got=%v", got)
			}
		})
	}
}
