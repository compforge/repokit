// Package callers locates incoming static calls at a declaration's name in a captured repository.
package callers

import (
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/code/navigation"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

type Request = navigation.Position

type Site struct {
	Caller cg.Node     `json:"caller"`
	Call   cg.Relation `json:"call"`
}

type Report struct {
	Snapshot    analysis.SnapshotReport `json:"snapshot"`
	Target      cg.Node                 `json:"target"`
	Callers     []Site                  `json:"callers"`
	Diagnostics []cg.Diagnostic         `json:"diagnostics,omitempty"`
}

// +spec=Select by declaration name range in one snapshot. Preserve each incoming Calls edge, including its precision and call site.
// +link=repo://docs/code.md
func Analyze(ctx context.Context, req Request) (Report, error) {
	decl, graph, err := navigation.Callable(ctx, req)
	report := Report{Snapshot: decl.Snapshot, Target: decl.Target, Diagnostics: decl.Diagnostics}
	if err != nil {
		return report, err
	}
	report.Callers = []Site{}
	for _, edge := range graph.RelationsTo(decl.Target.ID, cg.Calls) {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		caller, ok := graph.Node(edge.Source)
		if !ok {
			return Report{}, fmt.Errorf("missing caller node %s for call %s", edge.Source, edge.ID)
		}
		report.Callers = append(report.Callers, Site{Caller: caller, Call: edge})
	}
	return report, ctx.Err()
}
