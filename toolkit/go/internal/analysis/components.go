package analysis

import (
	"cmp"
	"slices"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/impact"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

type componentKey struct {
	repository common.Repository
	name, root string
}

type impactBucket struct {
	summary   project.ImpactSummary
	tags      map[string]map[cg.Tag]bool
	component *project.ComponentImpact
}

func newImpactBucket() *impactBucket {
	return &impactBucket{
		summary: project.ImpactSummary{
			AffectedFileCounts: map[cg.Tag]int{}, ChangedFiles: []string{}, AffectedFiles: []string{},
			SourceFiles: []string{}, TestFiles: []string{}, Scope: "focused", Complete: true,
		},
		tags: map[string]map[cg.Tag]bool{},
	}
}

func (b *impactBucket) count(name string, snapshot git.Snapshot) {
	b.summary.Affected = true
	if b.tags[name] == nil {
		b.tags[name] = map[cg.Tag]bool{}
	}
	for _, tag := range project.FileTags(cg.Document{Path: name, Gitlink: snapshot.Modules[name], Symlink: snapshot.Links[name]}) {
		if !b.tags[name][tag] {
			b.tags[name][tag] = true
			b.summary.AffectedFileCounts[tag]++
		}
	}
}

func (b *impactBucket) gap(message string) {
	b.summary.Complete = false
	b.summary.Scope = "partial"
	b.summary.FallbackReasons = append(b.summary.FallbackReasons, message)
}

func (b *impactBucket) finish() project.ImpactSummary {
	for _, paths := range []*[]string{&b.summary.ChangedFiles, &b.summary.AffectedFiles,
		&b.summary.SourceFiles, &b.summary.TestFiles, &b.summary.FallbackReasons} {
		slices.Sort(*paths)
		*paths = slices.Compact(*paths)
	}
	return b.summary
}

// componentResults projects every fact through its own version's file owner once.
// The unowned bucket preserves evidence without inventing a root Component.
// +spec=An unchanged component identity combines both versions, while moves and deletions retain old owners.
// +spec=Grouping changes neither graph admission nor test discovery; before-side paths are not runnable-file promises.
// +link=component://docs/diff.md
func componentResults(before, after project.Layout, c *comparison, result impact.Result, diagnostics []Diagnostic) ([]project.ComponentImpact, project.ImpactSummary) {
	buckets := map[componentKey]*impactBucket{}
	unowned := newImpactBucket()
	ensure := func(binding *project.Binding, version string) *impactBucket {
		if binding == nil {
			return unowned
		}
		key := componentKey{binding.Repository, binding.Name, binding.Root}
		if bucket := buckets[key]; bucket != nil {
			return bucket
		}
		bucket := newImpactBucket()
		bucket.component = &project.ComponentImpact{
			Component: binding.Component, Root: binding.Root, Snapshot: version,
			Products: binding.Products, ToolEvidence: binding.ToolEvidence,
		}
		buckets[key] = bucket
		return bucket
	}
	// Current metadata wins, even when all relevant evidence comes from before.
	for i := range after.Components {
		ensure(&after.Components[i], "after")
	}
	owner := func(name, version string) *impactBucket {
		if version == "before" {
			return ensure(before.Owner(name), version)
		}
		return ensure(after.Owner(name), "after")
	}
	sources := map[string]bool{}
	for _, name := range result.SourceFiles {
		sources[name] = true
	}
	for _, change := range c.changes {
		if change.Status != "deleted" {
			bucket := owner(change.Path, "after")
			bucket.summary.ChangedFiles = append(bucket.summary.ChangedFiles, change.Path)
			if sources[change.Path] {
				bucket.summary.SourceFiles = append(bucket.summary.SourceFiles, change.Path)
			}
			bucket.count(change.Path, c.after)
		}
		if change.Status != "added" {
			old := change.OldPath
			if old == "" {
				old = change.Path
			}
			bucket := owner(old, "before")
			bucket.summary.ChangedFiles = append(bucket.summary.ChangedFiles, old)
			if sources[change.Path] {
				bucket.summary.SourceFiles = append(bucket.summary.SourceFiles, old)
			}
			bucket.count(old, c.before)
		}
	}
	for _, file := range result.AffectedFiles {
		bucket := owner(file.Path, file.Version)
		bucket.summary.AffectedFiles = append(bucket.summary.AffectedFiles, file.Path)
		snapshot := c.after
		if file.Version == "before" {
			snapshot = c.before
		}
		bucket.count(file.Path, snapshot)
	}
	for _, name := range result.TestFiles {
		bucket := owner(name, "after")
		bucket.summary.TestFiles = append(bucket.summary.TestFiles, name)
		bucket.summary.Affected = true
	}
	repositoryGaps := []string{}
	for _, issue := range result.Uncertainties {
		message := issue.Message
		if issue.Path != "" {
			message = issue.Path + ": " + message
		}
		if issue.Scope == "repository" {
			repositoryGaps = append(repositoryGaps, message)
			continue
		}
		targets := map[*impactBucket]bool{}
		if issue.Path != "" {
			// A versioned gap follows that version; older/unversioned producers
			// conservatively retain both ownership possibilities.
			if issue.Version != "after" {
				targets[owner(issue.Path, "before")] = true
			}
			if issue.Version != "before" {
				targets[owner(issue.Path, "after")] = true
			}
		}
		for _, name := range issue.TestFiles {
			targets[owner(name, "after")] = true
		}
		for bucket := range targets {
			bucket.gap(message)
		}
	}
	// Capture gaps cannot be narrowed using an incomplete ownership observation.
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "impact_uncertain" {
			continue
		}
		repositoryGaps = append(repositoryGaps, diagnostic.Message)
	}
	for _, message := range repositoryGaps {
		for _, bucket := range buckets {
			bucket.gap(message)
		}
		unowned.gap(message)
	}
	components := make([]project.ComponentImpact, 0, len(buckets))
	for _, bucket := range buckets {
		bucket.component.ImpactSummary = bucket.finish()
		components = append(components, *bucket.component)
	}
	slices.SortFunc(components, func(a, b project.ComponentImpact) int {
		if order := cmp.Compare(a.Root, b.Root); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Snapshot, b.Snapshot); order != 0 {
			return order
		}
		return cmp.Compare(a.Component.Name, b.Component.Name)
	})
	return components, unowned.finish()
}
