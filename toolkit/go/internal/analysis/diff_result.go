package analysis

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"sort"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/repokit/toolkit/go/internal/diff"
	"github.com/compforge/repokit/toolkit/go/internal/observation"
	"github.com/compforge/repokit/toolkit/go/internal/units"
)

// DiffReport exposes captured changes and document tags without syntax, graph or impact analysis.
// Keep the result in memory when composing analyses: JSON is a display report,
// not a replacement for its retained repository snapshots. Changes may be filtered;
// their patch, source and version fields describe the captured input and are immutable.
type DiffReport struct {
	Schema                  int                `json:"schema"`
	Checkout                string             `json:"checkout"`
	Base                    string             `json:"base"`
	Head                    string             `json:"head,omitempty"`
	Input                   string             `json:"input"`
	BeforeSnapshot          string             `json:"before_snapshot"`
	AfterSnapshot           string             `json:"after_snapshot"`
	Changes                 []units.Change     `json:"changes"`
	Complete                bool               `json:"complete"`
	Diagnostics             []units.Diagnostic `json:"diagnostics"`
	tagRules                []cg.TagRule
	captured                *comparison
	beforeGraph, afterGraph *cg.Graph
}

// WithGraphs reuses caller-owned graphs for these exact versions. FormUnits
// validates fragment/graph version identity before using their relations.
func (d DiffReport) WithGraphs(before, after *cg.Graph) DiffReport {
	d.beforeGraph, d.afterGraph = before, after
	return d
}

// Select retains matching new or old paths while sharing the captured source.
// An empty selection means no changes, not every change.
func (d DiffReport) Select(paths ...string) DiffReport {
	selected := map[string]bool{}
	for _, p := range paths {
		selected[p] = true
	}
	changes := []units.Change{}
	for _, ch := range d.Changes {
		if selected[ch.Path()] || selected[ch.OldPath] {
			changes = append(changes, ch)
		}
	}
	d.Changes = changes
	return d
}

// SourceFile describes a regular file in the captured snapshot. Size is -1 when
// capture limits made its contents unavailable; no Git or filesystem read occurs.
type SourceFile struct {
	Path string
	Size int64
}

func (d DiffReport) SourceFiles(before bool) []SourceFile {
	if d.captured == nil {
		return nil
	}
	snapshot := d.captured.after
	if before {
		snapshot = d.captured.before
	}
	out := make([]SourceFile, 0, len(snapshot.Files))
	for p, b := range snapshot.Files {
		out = append(out, SourceFile{p, int64(len(b))})
	}
	for p := range snapshot.Opaque {
		out = append(out, SourceFile{p, -1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
func (d DiffReport) ReadSource(before bool, name string) (string, error) {
	if !diff.ValidPath(name) {
		return "", fmt.Errorf("invalid snapshot path %q", name)
	}
	if d.captured == nil {
		return "", fmt.Errorf("diff has no captured source snapshot")
	}
	snapshot := d.captured.after
	if before {
		snapshot = d.captured.before
	}
	if b, ok := snapshot.Files[name]; ok {
		return string(b), nil
	}
	if _, ok := snapshot.Opaque[name]; ok {
		return "", fmt.Errorf("%s: source exceeds capture limits", name)
	}
	return "", fmt.Errorf("%s: %w", name, fs.ErrNotExist)
}

// +spec=Document tags never discard changes, and classification uses Change.Path() in its owning snapshot.
func CaptureDiff(ctx context.Context, req Request) (report DiffReport, err error) {
	if len(req.TestDirs) > 0 {
		return report, fmt.Errorf("Diff does not select tests; use AnalyzeImpact")
	}
	tags, err := cg.NewTagMatcher(req.TagRules)
	if err != nil {
		return report, err
	}
	ref, _ := timeline.StageFromContext(ctx)
	ctx, stage := observation.Begin(ctx, ref.TimelineID, "diff.capture", timeline.WithParent(ref.StageID))
	defer func() { stage.End(err) }()
	c, err := captureComparison(ctx, req)
	if err != nil {
		return report, err
	}
	report = DiffReport{Schema: 1, Checkout: c.repo.Root, Base: c.base, Head: c.head, Input: c.input, BeforeSnapshot: c.before.Digest(), AfterSnapshot: c.after.Digest(), Changes: []units.Change{}, Diagnostics: []units.Diagnostic{}, captured: c}
	report.tagRules = slices.Clone(req.TagRules)
	for _, d := range c.changes {
		oldPath := d.OldPath
		if oldPath == "" {
			oldPath = d.Path
		}
		old, known := c.before.Files[oldPath]
		if target, ok := c.before.Links[oldPath]; ok {
			old, known = []byte(target), true
		}
		next, exists := c.after.Files[d.Path]
		if target, ok := c.after.Links[d.Path]; ok {
			next, exists = []byte(target), true
		}
		ch := units.Change{OldPath: oldPath, NewPath: d.Path, Diff: d.Text(next), OldFileContent: string(old), OldContentKnown: known, NewFileContent: string(next), NewContentMissing: !exists && d.Status != "deleted", BeforeRef: report.BeforeSnapshot, AfterRef: report.AfterSnapshot, IsNew: d.Status == "added", IsDeleted: d.Status == "deleted", IsRenamed: d.Status == "renamed", IsBinary: d.Binary}
		if ch.IsDeleted {
			ch.NewPath = "/dev/null"
		}
		if ch.IsNew {
			ch.OldPath = "/dev/null"
		}
		snapshot := c.after
		if ch.IsDeleted {
			snapshot = c.before
		}
		ch.Tags = tags.DocumentTags(cg.Document{Path: ch.Path(), Gitlink: snapshot.Modules[ch.Path()]})
		for _, h := range d.Hunks {
			ch.Insertions += int64(h.New.Count)
			ch.Deletions += int64(h.Old.Count)
		}
		report.Changes = append(report.Changes, ch)
	}

	for _, issue := range c.issues {
		report.Diagnostics = append(report.Diagnostics, units.Diagnostic{Code: "snapshot_incomplete", Message: issue})
	}
	for _, ch := range report.Changes {
		if _, oldOpaque := c.before.Opaque[ch.OldPath]; oldOpaque || c.after.Opaque[ch.NewPath] != "" {
			report.Diagnostics = append(report.Diagnostics, units.Diagnostic{Code: "source_unavailable", Path: ch.Path(), Message: "source exceeds capture limits"})
		}
	}
	changed, err := c.changed(ctx, req)
	if err != nil {
		return report, err
	}
	if changed {
		report.Diagnostics = append(report.Diagnostics, units.Diagnostic{Code: "snapshot_changed", Message: "repository contents changed during capture"})
	}
	report.Complete = len(report.Diagnostics) == 0
	return report, nil
}

// CaptureDiffSnapshots retains both source views for analyses composed with a diff.
// Consumers treat the reports and contents as read-only; no second capture occurs.
// +spec=`Composed analyses use the exact snapshots retained by their comparison`
func CaptureDiffSnapshots(ctx context.Context, req Request) (report DiffReport, before, after *CapturedSnapshot, err error) {
	report, err = CaptureDiff(ctx, req)
	if err != nil {
		return
	}
	c := report.captured
	before = capturedReport(c.before, c.repo.Root, "commit", c.base)
	after = capturedReport(c.after, c.repo.Root, c.input, c.head)
	return
}
