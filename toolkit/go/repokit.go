// Package repokit provides read-only repository analysis and explicit Git and
// Makefile operations. Applications consume this module through its public API.
// Operations may invoke Git or declared Make targets, never the repocli executable.
// Callers own context deadlines, persistence and workflow policy.
package repokit

import (
	"context"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/impact"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// InputRequest selects a working tree, index (Staged), or exact commit/ref (Head).
// Repository is a checkout path; an empty path selects the current directory.
type InputRequest = analysis.InputRequest

// InspectReport describes organization and observation completeness. Snapshot
// identifies retained contents when obtained through DiffReport.Inspect.
type InspectReport = analysis.InspectReport

// Layout describes repository identity and component boundaries. Owner accepts a
// repository-relative path, including a deleted path, and returns its deepest component.
type Layout = project.Layout

// ComponentBinding extends common.Component with its observed root, products and tool evidence.
// Repository, Component and Product identities remain quality-harness common types.
type ComponentBinding = project.Binding

// Diagnostic records an observed gap; a successful call need not be complete.
type Diagnostic = analysis.Diagnostic

// SnapshotReport identifies captured contents and their completeness.
type SnapshotReport = analysis.SnapshotReport

// DiffRequest selects a baseline and a working-tree, index, commit or patch comparison.
// Base defaults to HEAD. PatchFile "-" reads Stdin supplied by the caller.
type DiffRequest = analysis.Request

// Default impact budgets apply independently to each comparison version.
const (
	DefaultImpactMaxFiles     = impact.DefaultMaxFiles
	DefaultImpactMaxNodes     = impact.DefaultMaxNodes
	DefaultImpactMaxRelations = impact.DefaultMaxRelations
)

// DiffReport retains source snapshots for subsequent analysis. Its Inspect method
// describes either captured layout without reopening the repository.
type DiffReport = analysis.DiffReport
type SourceFile = analysis.SourceFile

// ImpactReport contains impact evidence and test recommendations, not a test verdict.
type ImpactReport = analysis.Report

// Uncertainty describes a scoped static-analysis observation or coverage gap.
type Uncertainty = impact.Uncertainty

// ComponentImpact retains ownership and impact from each comparison version.
type ComponentImpact = project.ComponentImpact

// ImpactSummary describes component-owned or unowned files, counts and analysis gaps.
type ImpactSummary = project.ImpactSummary

// GraphSnapshot keeps nodes, relations and source bytes tied to one content version.
// Its data is caller-owned and should be treated as read-only.
type GraphSnapshot = analysis.GraphSnapshot

// Inspect reads the Git file catalog and organization metadata without capturing
// source bytes, computing a digest or building a graph.
func Inspect(ctx context.Context, req InputRequest) (InspectReport, error) {
	result, err := analysis.Inspect(ctx, req)
	return result, errs.Wrap(err, "inspect.failed", "inspect", req.Repository)
}

// Snapshot captures content identity independently of organization configuration.
func Snapshot(ctx context.Context, req InputRequest) (SnapshotReport, error) {
	result, err := analysis.CaptureSnapshot(ctx, req)
	return result, errs.Wrap(err, "snapshot.failed", "snapshot", req.Repository)
}

// Diff captures changed files, patches, versioned source and document tags
// without parsing source or building a graph.
func Diff(ctx context.Context, req DiffRequest) (DiffReport, error) {
	result, err := analysis.CaptureDiff(ctx, req)
	return result, errs.Wrap(err, "diff.failed", "diff", req.Repository)
}

// AnalyzeImpact preserves the independent impact/test-selection capability.
func AnalyzeImpact(ctx context.Context, req DiffRequest) (ImpactReport, error) {
	result, err := analysis.Analyze(ctx, req)
	return result, errs.Wrap(err, "impact.failed", "impact", req.Repository)
}

// Graph captures source bytes and builds a graph from the same version.
// maxDocuments must be positive; omitted documents are reported as diagnostics.
func Graph(ctx context.Context, req InputRequest, maxDocuments int) (*GraphSnapshot, error) {
	result, err := analysis.CaptureGraph(ctx, req, maxDocuments)
	return result, errs.Wrap(err, "graph.failed", "graph", req.Repository)
}

// TreeReport describes entries independently of Component discovery.
type TreeReport = analysis.TreeReport
type Directory = analysis.Directory

// GitEntry retains literal path, entry kind, mode and observed object ID.
type GitEntry = git.Entry
type File = analysis.File
type Manifest = analysis.Manifest

// Tree observes paths, kinds and known file roles in the selected version.
func Tree(ctx context.Context, req InputRequest) (TreeReport, error) {
	result, err := analysis.Tree(ctx, req)
	return result, errs.Wrap(err, "tree.failed", "tree", req.Repository)
}

// GraphSource is an existing read-only material view; graph construction never recaptures it.
type GraphSource = analysis.GraphSource

// SourceDirectory contains only immediate children of a requested directory.
type SourceDirectory = analysis.SourceDirectory

// SourceTree indexes caller-owned captured files without reopening their origin.
type SourceTree = analysis.SourceTree

func NewSourceTree(files []SourceFile) (*SourceTree, error) {
	result, err := analysis.NewSourceTree(files)
	return result, errs.Wrap(err, "graph.invalid_source", "graph.source", "")
}

// GraphOptions configures material admission, priority, and CodeGraph budgets.
type GraphOptions = analysis.GraphOptions

// GraphResult retains a publication and its input coverage.
type GraphResult = analysis.GraphResult

// OpenGraphSource selects a Git revision/index or live source directory.
func OpenGraphSource(ctx context.Context, req InputRequest) (GraphSource, error) {
	result, err := analysis.OpenGraphSource(ctx, req)
	return result, errs.Wrap(err, "graph.source_failed", "graph.source", req.Repository)
}

// GraphFromSource publishes code and sources from the caller's existing view.
func GraphFromSource(ctx context.Context, source GraphSource, opts GraphOptions) (*GraphResult, error) {
	result, err := analysis.GraphFromSource(ctx, source, opts)
	return result, errs.Wrap(err, "graph.failed", "graph", "")
}

// IsSourceMaterial recognizes source/manifest formats; GraphOptions controls admission.
func IsSourceMaterial(name string) bool { return project.IsSourceMaterial(name) }

// SourceRoles provides conventional file roles and semantic roles from an optional graph.
func SourceRoles(name string, g *cg.Graph) []string { return project.SourceRoles(name, g) }
