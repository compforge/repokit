package deadcode

import (
	"context"
	"fmt"
	"slices"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/impact"
)

// +spec=`Candidate scope never removes callers from either snapshot's graph`
func Analyze(ctx context.Context, req Request) (Report, error) {
	if err := req.Validate(); err != nil {
		return Report{}, err
	}
	if req.Mode == "" {
		req.Mode = "unreferenced"
	}
	before, after, scope, renames, err := captureDeadcode(ctx, req)
	if err != nil {
		return Report{}, err
	}
	build := func(c *analysis.CapturedSnapshot) (*analysis.GraphSnapshot, error) {
		return c.Graph(ctx, max(1, len(c.Contents.Files)), shared.Options{MaxNodes: req.MaxNodes, MaxRelations: req.MaxRelations})
	}
	graph, err := build(after)
	if err != nil {
		return Report{}, err
	}
	current, err := inspect(ctx, graph, req)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: 2, Mode: req.Mode, Scope: scope, Snapshot: graph.Snapshot,
		Documents: graph.Documents, Nodes: current.candidates, Roots: current.roots, Diagnostics: graph.Diagnostics}
	if before != nil {
		previousGraph, err := build(before)
		if err != nil {
			return Report{}, fmt.Errorf("before graph: %w", err)
		}
		previous, err := inspect(ctx, previousGraph, req)
		if err != nil {
			return Report{}, fmt.Errorf("before analysis: %w", err)
		}
		report.Baseline = &Baseline{Snapshot: previousGraph.Snapshot, Documents: previousGraph.Documents, Diagnostics: previousGraph.Diagnostics, Roots: previous.roots}
		report.Nodes, err = compare(ctx, previous, current, scope.ChangedFiles, renames)
		if err != nil {
			return Report{}, err
		}
	}
	report.Nodes = filterCandidates(report.Nodes, req)
	report.Nodes = slices.DeleteFunc(report.Nodes, func(candidate Candidate) bool {
		node := candidate.Node
		if req.Files != nil && (node.Location == nil || !slices.Contains(req.Files, node.Location.Path)) {
			return true
		}
		return false
	})
	for i := range report.Nodes {
		if !scope.Complete {
			report.Nodes[i].Risks = append(report.Nodes[i].Risks, "incomplete_comparison")
		}
	}
	return report, nil
}

// +spec=Candidate filters preserve callers from tests and runtime entrypoints in the full graph.
func filterCandidates(nodes []Candidate, req Request) []Candidate {
	if !req.ExcludeTests && !req.ExcludeEntrypoints {
		return nodes
	}
	return slices.DeleteFunc(nodes, func(node Candidate) bool {
		if req.ExcludeTests && node.Location != nil && impact.IsTest(node.Location.Path) {
			return true
		}
		// CodeGraph owns language rules; report policy only consumes its facts.
		return req.ExcludeEntrypoints && node.Entrypoint
	})
}
