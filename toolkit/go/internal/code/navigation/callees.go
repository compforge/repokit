package navigation

import (
	"context"
	"fmt"

	cg "github.com/compforge/codegraph"
)

type CalleeSite struct {
	Callee cg.Node     `json:"callee"`
	Call   cg.Relation `json:"call"`
}

type CalleesReport struct {
	Declaration
	Callees []CalleeSite `json:"callees"`
}

// +spec=Each outgoing Calls edge is one direct call site owned by the selected declaration.
func Callees(ctx context.Context, req Position) (CalleesReport, error) {
	decl, graph, err := Callable(ctx, req)
	report := CalleesReport{Declaration: decl}
	if err != nil {
		return report, err
	}
	report.Callees = []CalleeSite{}
	for _, edge := range graph.RelationsFrom(decl.Target.ID, cg.Calls) {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		target, ok := graph.Node(edge.Target)
		if !ok {
			return report, fmt.Errorf("missing callee node %s for call %s", edge.Target, edge.ID)
		}
		report.Callees = append(report.Callees, CalleeSite{Callee: target, Call: edge})
	}
	return report, ctx.Err()
}
