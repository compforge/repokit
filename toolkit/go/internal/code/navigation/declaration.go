// Package navigation queries declaration relationships in a captured CodeGraph.
package navigation

import (
	"context"
	"fmt"
	"io/fs"
	"slices"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

type Position struct {
	analysis.InputRequest
	Path         string
	Line, Column int
}

func (r Position) Validate() error {
	if !fs.ValidPath(r.Path) || r.Path == "." || r.Line <= 0 || r.Column <= 0 {
		return fmt.Errorf("expected a repository-relative file and positive line and byte column")
	}
	return nil
}

type Declaration struct {
	Snapshot    analysis.SnapshotReport `json:"snapshot"`
	Target      cg.Node                 `json:"target"`
	Diagnostics []cg.Diagnostic         `json:"diagnostics,omitempty"`
}

func Callable(ctx context.Context, req Position) (Declaration, *cg.Graph, error) {
	return resolve(ctx, req, "callable", cg.Function, cg.Method, cg.Constructor)
}

func resolve(ctx context.Context, req Position, description string, kinds ...cg.NodeKind) (Declaration, *cg.Graph, error) {
	if err := req.Validate(); err != nil {
		return Declaration{}, nil, err
	}
	captured, err := analysis.Capture(ctx, req.InputRequest)
	if err != nil {
		return Declaration{}, nil, err
	}
	report := Declaration{Snapshot: captured.Report}
	_, retained := captured.Contents.Files[req.Path]
	_, opaque := captured.Contents.Opaque[req.Path]
	if !retained && !opaque {
		return report, nil, fmt.Errorf("file %q unavailable in selected snapshot", req.Path)
	}
	result, err := captured.BuildGraph(ctx, max(1, len(captured.Contents.Files)), cg.Options{MaxNodes: 250000, MaxRelations: 1000000, Admission: cg.AnalysisAdmission()})
	if err != nil {
		return report, nil, err
	}
	report.Diagnostics = append(result.InputDiagnostics, result.Report.Diagnostics...)
	if _, ok := result.Sources[req.Path]; !ok {
		for _, d := range result.InputDiagnostics {
			if d.Location.Path == req.Path {
				return report, nil, fmt.Errorf("file omitted from code graph: %s", d.Message)
			}
		}
		if opaque {
			return report, nil, fmt.Errorf("file %q exceeds the snapshot file byte limit", req.Path)
		}
		return report, nil, fmt.Errorf("file %q omitted from code graph", req.Path)
	}
	var target cg.Node
	for _, n := range result.Graph.Find(req.Path, "", "") {
		if !slices.Contains(kinds, n.Kind) {
			continue
		}
		loc := n.NameLocation
		if loc == nil || req.Line < loc.Line || req.Line > loc.EndLine || (req.Line == loc.Line && req.Column < loc.Column) || (req.Line == loc.EndLine && req.Column >= loc.EndColumn) {
			continue
		}
		if target.ID != "" {
			return report, nil, fmt.Errorf("ambiguous declaration at %s:%d:%d", req.Path, req.Line, req.Column)
		}
		target = n
	}
	if target.ID == "" {
		return report, nil, fmt.Errorf("no indexed %s declaration name at %s:%d:%d", description, req.Path, req.Line, req.Column)
	}
	report.Target = target
	return report, result.Graph, nil
}
