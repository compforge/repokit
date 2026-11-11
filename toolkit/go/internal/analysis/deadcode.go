package analysis

import (
	"context"
	"fmt"
	"slices"

	"github.com/compforge/repokit/toolkit/go/internal/diff"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/impact"
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

// DeadcodeCandidate records an observation, never a safe-deletion verdict.
type DeadcodeCandidate struct {
	shared.Node
	Reason string   `json:"reason"`
	Risks  []string `json:"risks"`
	// Change distinguishes introduced, became_dead, became_test_only, pre_existing,
	// and unmatched candidates in diff reports.
	Change   string       `json:"change,omitempty"`
	Previous *shared.Node `json:"previous,omitempty"`
	// LostUses are direct incoming dependencies present only in the before graph.
	// Transitive loss of reachability need not remove a direct incoming edge.
	LostUses []DeadcodeUse `json:"lostUses"`
}

type DeadcodeUse struct {
	Source     shared.Node         `json:"source"`
	Kind       shared.RelationKind `json:"kind"`
	Location   *shared.Location    `json:"location,omitempty"`
	Confidence shared.Confidence   `json:"confidence"`
}

type DeadcodeRoot struct {
	shared.Node
	Reason string `json:"reason"`
}

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
	current, err := inspectDeadcode(ctx, graph, req)
	if err != nil {
		return DeadcodeReport{}, err
	}
	report := DeadcodeReport{SchemaVersion: 2, Mode: req.Mode, Scope: scope, Snapshot: graph.Snapshot,
		Documents: graph.Documents, Nodes: current.candidates, Roots: current.roots, Diagnostics: graph.Diagnostics}
	if before != nil {
		previousGraph, err := build(before)
		if err != nil {
			return DeadcodeReport{}, fmt.Errorf("before graph: %w", err)
		}
		previous, err := inspectDeadcode(ctx, previousGraph, req)
		if err != nil {
			return DeadcodeReport{}, fmt.Errorf("before analysis: %w", err)
		}
		report.Baseline = &DeadcodeBaseline{Snapshot: previousGraph.Snapshot, Documents: previousGraph.Documents, Diagnostics: previousGraph.Diagnostics, Roots: previous.roots}
		report.Nodes, err = changedDeadcode(ctx, previous, current, scope.ChangedFiles, renames)
		if err != nil {
			return DeadcodeReport{}, err
		}
	}
	report.Nodes = filterDeadcodeCandidates(report.Nodes, req)
	report.Nodes = slices.DeleteFunc(report.Nodes, func(candidate DeadcodeCandidate) bool {
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

// +spec=`Deadcode candidates have no incoming non-structural relation, regardless of confidence`
// This is an observation about the captured graph, not proof of safe deletion.
func unreferencedDeclarations(ctx context.Context, graph *GraphSnapshot) ([]shared.Node, error) {
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
	slices.SortFunc(nodes, compareDeadcodeNodes)
	return nodes, nil
}

// +spec=Candidate filters preserve callers from tests and runtime entrypoints in the full graph.
func filterDeadcodeCandidates(nodes []DeadcodeCandidate, req DeadcodeRequest) []DeadcodeCandidate {
	if !req.ExcludeTests && !req.ExcludeEntrypoints {
		return nodes
	}
	return slices.DeleteFunc(nodes, func(node DeadcodeCandidate) bool {
		if req.ExcludeTests && node.Location != nil && impact.IsTest(node.Location.Path) {
			return true
		}
		// CodeGraph owns language rules; report policy only consumes its facts.
		return req.ExcludeEntrypoints && node.Entrypoint
	})
}
