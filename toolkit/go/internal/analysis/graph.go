package analysis

import (
	"context"
	"fmt"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/codegraph/semantics/nodejs"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// GraphSnapshot retains the source bytes behind a published graph. The viewer
// consumes shared graph facts directly, without the impact-specific projection.
// +spec=`Graph nodes, relations and displayed source belong to one captured snapshot`
type GraphSnapshot struct {
	project.Layout
	Snapshot    SnapshotReport      `json:"snapshot"`
	Nodes       []shared.Node       `json:"nodes"`
	Relations   []shared.Relation   `json:"relations"`
	Diagnostics []shared.Diagnostic `json:"diagnostics"`
	Documents   int                 `json:"documents"`
	Sources     map[string][]byte   `json:"-"`
}

func CaptureGraph(ctx context.Context, req InputRequest, maxDocuments int) (*GraphSnapshot, error) {
	if maxDocuments <= 0 {
		return nil, fmt.Errorf("document limit must be positive")
	}
	captured, err := Capture(ctx, req)
	if err != nil {
		return nil, err
	}
	// Full repository graphs include source-use nodes and relations; the
	// general-purpose CodeGraph defaults can exhaust these before binding ends.
	return captured.Graph(ctx, maxDocuments, shared.Options{
		MaxNodes: 250000, MaxRelations: 1000000, ResourceReferences: nodejs.ResourceReferences(),
	})
}

// Graph builds from this retained capture without selecting or rereading repository contents.
// +spec=Shared tooling tags exclude graph material before document budgets while the captured snapshot remains intact.
// +spec=Only source material is parsed for symbols; other documents remain context and recognized manifests supply metadata.
// +link=repo://docs/view.md
func (captured *CapturedSnapshot) Graph(ctx context.Context, maxDocuments int, opts shared.Options) (*GraphSnapshot, error) {
	report, contents := captured.Report, captured.Contents
	repo := &git.Repository{Root: report.Checkout}
	origin, err := repo.Origin(ctx)
	if err != nil {
		return nil, err
	}
	layout, err := project.Load(contents.Files, origin)
	if err != nil {
		return nil, err
	}
	if opts.Admission == nil {
		opts.Admission = &shared.AdmissionPolicy{ParseTags: []shared.Tag{shared.SourceTag}, ExcludedTags: []shared.Tag{shared.ToolingTag}}
	}
	opts.MaxDocuments = maxDocuments
	if opts.MaxDocumentBytes == 0 {
		opts.MaxDocumentBytes = 2 << 20
	}
	opts.MaxSourceBytes = 128 << 20
	source, err := newRetainedGraphSource(report.Snapshot, contents)
	if err != nil {
		return nil, err
	}
	result, err := GraphFromSource(ctx, source, GraphOptions{CodeGraph: opts})
	if err != nil {
		return nil, err
	}
	diagnostics := append(result.InputDiagnostics, result.Report.Diagnostics...)
	if diagnostics == nil {
		diagnostics = []shared.Diagnostic{}
	}
	return &GraphSnapshot{Layout: layout, Snapshot: report, Nodes: result.Graph.Nodes(), Relations: result.Graph.Relations(), Diagnostics: diagnostics, Documents: result.Documents, Sources: result.Sources}, nil
}
