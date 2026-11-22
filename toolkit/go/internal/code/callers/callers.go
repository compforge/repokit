// Package callers locates incoming static calls at a declaration's name in a captured repository.
package callers

import (
	"context"
	"fmt"
	"io/fs"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

type Request struct {
	analysis.InputRequest
	Path         string
	Line, Column int
}

func (r Request) Validate() error {
	if !fs.ValidPath(r.Path) || r.Path == "." || r.Line <= 0 || r.Column <= 0 {
		return fmt.Errorf("expected a repository-relative file and positive line and byte column")
	}
	return nil
}

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
	if err := req.Validate(); err != nil {
		return Report{}, err
	}
	captured, err := analysis.Capture(ctx, req.InputRequest)
	if err != nil {
		return Report{}, err
	}
	report := Report{Snapshot: captured.Report}
	_, retained := captured.Contents.Files[req.Path]
	_, opaque := captured.Contents.Opaque[req.Path]
	if !retained && !opaque {
		return report, fmt.Errorf("file %q unavailable in selected snapshot", req.Path)
	}
	result, err := captured.BuildGraph(ctx, max(1, len(captured.Contents.Files)), cg.Options{MaxNodes: 250000, MaxRelations: 1000000, Admission: cg.AnalysisAdmission()})
	if err != nil {
		return report, err
	}
	report.Diagnostics = append(result.InputDiagnostics, result.Report.Diagnostics...)
	if _, ok := result.Sources[req.Path]; !ok {
		for _, d := range result.InputDiagnostics {
			if d.Location.Path == req.Path {
				return report, fmt.Errorf("file omitted from code graph: %s", d.Message)
			}
		}
		if opaque {
			return report, fmt.Errorf("file %q exceeds the snapshot file byte limit", req.Path)
		}
		return report, fmt.Errorf("file %q omitted from code graph", req.Path)
	}
	var target cg.Node
	for _, n := range result.Graph.Find(req.Path, "", "") {
		switch n.Kind {
		case cg.Function, cg.Method, cg.Constructor:
		default:
			continue
		}
		loc := n.NameLocation
		if loc == nil || req.Line < loc.Line || req.Line > loc.EndLine || (req.Line == loc.Line && req.Column < loc.Column) || (req.Line == loc.EndLine && req.Column >= loc.EndColumn) {
			continue
		}
		if target.ID != "" {
			return report, fmt.Errorf("ambiguous declaration at %s:%d:%d", req.Path, req.Line, req.Column)
		}
		target = n
	}
	if target.ID == "" {
		return report, fmt.Errorf("no indexed callable declaration name at %s:%d:%d", req.Path, req.Line, req.Column)
	}
	report.Target = target
	report.Callers = []Site{}
	for _, edge := range result.Graph.RelationsTo(target.ID, cg.Calls) {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		caller, ok := result.Graph.Node(edge.Source)
		if !ok {
			return Report{}, fmt.Errorf("missing caller node %s for call %s", edge.Source, edge.ID)
		}
		report.Callers = append(report.Callers, Site{Caller: caller, Call: edge})
	}
	return report, ctx.Err()
}
