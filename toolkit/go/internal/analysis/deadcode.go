package analysis

import (
	"context"
	"fmt"

	"github.com/compforge/repokit/toolkit/go/internal/diff"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/deadcode"
)

// DeadcodeRequest selects repository contents and finite graph build budgets.
// Zero budgets retain CodeGraph defaults.
type DeadcodeRequest struct {
	InputRequest
	MaxNodes     int
	MaxRelations int
	// Base compares before/after graphs and reports candidates reached from changed documents.
	Base string
	// Files limits returned candidates, never graph inputs. Nil means all files;
	// an explicitly empty slice selects no files.
	Files []string
	// Mode is unreferenced (default) or unreachable. Roots selects additional
	// graph roots by exact node ID or unique qualified name in unreachable mode.
	Mode  string
	Roots []string
	// Filters affect returned candidates only; all captured documents still enter the graph.
	ExcludeTests       bool
	ExcludeEntrypoints bool
}

func (req DeadcodeRequest) Validate() error {
	if req.MaxNodes < 0 || req.MaxRelations < 0 {
		return fmt.Errorf("MaxNodes and MaxRelations must not be negative")
	}
	if req.Head != "" && req.Staged {
		return fmt.Errorf("head and staged are mutually exclusive")
	}
	if req.Mode != "" && req.Mode != "unreferenced" && req.Mode != "unreachable" {
		return fmt.Errorf("mode must be unreferenced or unreachable")
	}
	if len(req.Roots) > 0 && req.Mode != "unreachable" {
		return fmt.Errorf("roots require unreachable mode")
	}
	for _, file := range req.Files {
		if !diff.ValidPath(file) {
			return fmt.Errorf("invalid candidate file %q", file)
		}
	}
	for _, root := range req.Roots {
		if root == "" {
			return fmt.Errorf("root must not be empty")
		}
	}
	return nil
}

type DeadcodeCandidate = deadcode.Candidate
type DeadcodeUse = deadcode.Use
type DeadcodeRoot = deadcode.Root

// DeadcodeScope describes candidate selection independently of captured material.
type DeadcodeScope struct {
	Kind         string       `json:"kind"`
	Files        []string     `json:"files"`
	ChangedFiles []string     `json:"changedFiles"`
	Complete     bool         `json:"complete"`
	Diagnostics  []Diagnostic `json:"diagnostics"`
}

type DeadcodeBaseline struct {
	Snapshot    SnapshotReport      `json:"snapshot"`
	Documents   int                 `json:"documents"`
	Diagnostics []shared.Diagnostic `json:"diagnostics"`
	Roots       []DeadcodeRoot      `json:"roots"`
}

// DeadcodeReport retains independent capture, graph and comparison gaps.
type DeadcodeReport struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Mode          string              `json:"mode"`
	Scope         DeadcodeScope       `json:"scope"`
	Snapshot      SnapshotReport      `json:"snapshot"`
	Documents     int                 `json:"documents"`
	Nodes         []DeadcodeCandidate `json:"nodes"`
	Roots         []DeadcodeRoot      `json:"roots"`
	Diagnostics   []shared.Diagnostic `json:"diagnostics"`
	Baseline      *DeadcodeBaseline   `json:"baseline,omitempty"`
}

// +spec=`Candidate scope never removes callers from either snapshot's graph`
func AnalyzeDeadcode(ctx context.Context, req DeadcodeRequest) (DeadcodeReport, error) {
	if err := req.Validate(); err != nil {
		return DeadcodeReport{}, err
	}
	if req.Mode == "" {
		req.Mode = "unreferenced"
	}
	before, after, scope, renames, err := captureDeadcode(ctx, req)
	if err != nil {
		return DeadcodeReport{}, err
	}
	build := func(c *CapturedSnapshot) (*GraphSnapshot, error) {
		return buildGraphSnapshot(ctx, c, max(1, len(c.Contents.Files)), shared.Options{MaxNodes: req.MaxNodes, MaxRelations: req.MaxRelations})
	}
	graph, err := build(after)
	if err != nil {
		return DeadcodeReport{}, err
	}
	report := DeadcodeReport{SchemaVersion: 2, Mode: req.Mode, Scope: scope, Snapshot: graph.Snapshot,
		Documents: graph.Documents, Diagnostics: graph.Diagnostics}
	var comparison *deadcode.Comparison
	if before != nil {
		previousGraph, err := build(before)
		if err != nil {
			return DeadcodeReport{}, fmt.Errorf("before graph: %w", err)
		}
		comparison = &deadcode.Comparison{Before: deadcodeFacts(previousGraph), ChangedFiles: scope.ChangedFiles, Renames: renames, Complete: scope.Complete}
		report.Baseline = &DeadcodeBaseline{Snapshot: previousGraph.Snapshot, Documents: previousGraph.Documents, Diagnostics: previousGraph.Diagnostics}
	}
	result, err := deadcode.Analyze(ctx, deadcodeFacts(graph), comparison, deadcode.Options{
		Mode: req.Mode, Roots: req.Roots, Files: req.Files, ExcludeTests: req.ExcludeTests, ExcludeEntrypoints: req.ExcludeEntrypoints,
	})
	if err != nil {
		return DeadcodeReport{}, err
	}
	report.Nodes, report.Roots = result.Candidates, result.Roots
	if report.Baseline != nil {
		report.Baseline.Roots = result.BeforeRoots
	}
	return report, nil
}

func deadcodeFacts(graph *GraphSnapshot) deadcode.Graph {
	return deadcode.Graph{Nodes: graph.Nodes, Relations: graph.Relations, Diagnostics: graph.Diagnostics, CaptureComplete: graph.Snapshot.Complete}
}
