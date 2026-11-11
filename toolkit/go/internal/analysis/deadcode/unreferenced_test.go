package deadcode

import (
	"context"
	"errors"
	"reflect"
	"testing"

	cg "github.com/compforge/codegraph/pkg"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

func TestDeadcodeIncomingUsageRule(t *testing.T) {
	graph := &analysis.GraphSnapshot{}
	for _, id := range []string{"unused", "called", "referenced", "weak", "self", "exported", "base", "decorated", "package"} {
		kind := cg.Function
		if id == "package" {
			kind = cg.Package
		}
		graph.Nodes = append(graph.Nodes, cg.Node{ID: id, Kind: kind})
		graph.Relations = append(graph.Relations, cg.Relation{Source: "doc", Target: id, Kind: cg.Declares})
	}
	// Source-use nodes are not declarations, even when they have no incoming edge.
	graph.Nodes = append(graph.Nodes, cg.Node{ID: "reference", Kind: cg.ReadSite})
	graph.Relations = append(graph.Relations,
		cg.Relation{Source: "owner", Target: "unused", Kind: cg.Contains},
		cg.Relation{Source: "doc", Target: "unused", Kind: cg.Encloses},
		cg.Relation{Source: "reference", Target: "unused", Kind: cg.OccursIn},
		cg.Relation{Source: "caller", Target: "called", Kind: cg.Calls},
		cg.Relation{Source: "reference", Target: "referenced", Kind: cg.BindsTo},
		cg.Relation{Source: "caller", Target: "weak", Kind: cg.Calls, Confidence: cg.NameOnly},
		cg.Relation{Source: "self", Target: "self", Kind: cg.Calls},
		cg.Relation{Source: "export", Target: "exported", Kind: cg.Aliases},
		cg.Relation{Source: "child", Target: "base", Kind: cg.Extends},
		cg.Relation{Source: "annotation", Target: "decorated", Kind: cg.Decorates},
	)
	nodes, err := unreferencedDeclarations(context.Background(), graph)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	if !reflect.DeepEqual(ids, []string{"unused"}) {
		t.Fatal(ids)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := unreferencedDeclarations(ctx, graph); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
