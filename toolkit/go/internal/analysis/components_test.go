package analysis

import (
	"reflect"
	"slices"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/diff"
	"github.com/compforge/repokit/toolkit/go/internal/impact"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

func TestComponentImpactIncludesChangesAndReachableFilesWithoutTests(t *testing.T) {
	layout, err := project.Load(map[string][]byte{"lib/go.mod": nil, "app/go.mod": nil, "other/go.mod": nil}, "")
	if err != nil {
		t.Fatal(err)
	}
	result := impact.Result{AffectedFiles: []impact.FileImpact{{Path: "app/use.go", Version: "after"}, {Path: "app/use.go", Version: "before"}}}
	groups, _ := componentResults(layout, layout, &comparison{changes: []diff.Change{{Path: "lib/config.json", Status: "modified"}}}, result, nil)
	for _, g := range groups {
		switch g.Root {
		case "lib":
			if !g.Affected || !slices.Equal(g.ChangedFiles, []string{"lib/config.json"}) {
				t.Fatal(g)
			}
		case "app":
			if !g.Affected || !slices.Equal(g.AffectedFiles, []string{"app/use.go"}) || len(g.TestFiles) != 0 {
				t.Fatal(g)
			}
		case "other":
			if g.Affected || !g.Complete {
				t.Fatal(g)
			}
		}
	}
}

func TestComponentImpactRetainsRenameAndDeletedOwnership(t *testing.T) {
	before, _ := project.Load(map[string][]byte{"old/go.mod": nil, "app/go.mod": nil}, "")
	after, _ := project.Load(map[string][]byte{"new/go.mod": nil, "app/go.mod": nil}, "")
	groups, _ := componentResults(before, after, &comparison{changes: []diff.Change{{Path: "new/data.json", OldPath: "old/data.json", Status: "renamed"}}}, impact.Result{}, nil)
	for _, g := range groups {
		if g.Root == "app" {
			if g.Affected {
				t.Fatal(g)
			}
			continue
		}
		if !g.Affected || !slices.Equal(g.ChangedFiles, []string{g.Root + "/data.json"}) {
			t.Fatal(g)
		}
		if g.Root == "old" && g.Snapshot != "before" {
			t.Fatal(g)
		}
	}
}

func TestUnknownComponentImpactIsNotClaimedUnaffected(t *testing.T) {
	layout, _ := project.Load(map[string][]byte{"lib/go.mod": nil, "other/go.mod": nil}, "")
	for _, scope := range []string{"component", "repository"} {
		result := impact.Result{Uncertainties: []impact.Uncertainty{{Scope: scope, Path: "lib/config.json", Message: "unknown configuration effect"}}}
		groups, _ := componentResults(layout, layout, &comparison{}, result, nil)
		for _, g := range groups {
			unknown := scope == "repository" || g.Root == "lib"
			if g.Affected || g.Complete == unknown || (len(g.FallbackReasons) > 0) != unknown {
				t.Fatal(g)
			}
		}
	}
}

// +case=component_affected_file_counts_versions
func TestComponentAffectedFileCountsDeduplicatePathsAndPreserveVersionOwnership(t *testing.T) {
	before, _ := project.Load(map[string][]byte{"old/go.mod": nil, "new/go.mod": nil, "same/go.mod": nil}, "")
	after, _ := project.Load(map[string][]byte{"new/go.mod": nil, "same/go.mod": nil}, "")
	c := &comparison{changes: []diff.Change{
		{Path: "new/guide.md", OldPath: "old/work.go", Status: "renamed"},
		{Path: "new/data.mystery", OldPath: "same/data.mystery", Status: "renamed"},
		{Path: "same/deleted.go", Status: "deleted"},
		{Path: "same/work_test.go", Status: "modified"},
		{Path: "same/guide.md", OldPath: "same/original.go", Status: "renamed"},
		{Path: "same/link.go", Status: "modified"},
	}}
	// A gitlink becoming a regular source retains both classifications, once each.
	c.before.Modules = map[string]string{"same/link.go": "revision"}
	result := impact.Result{AffectedFiles: []impact.FileImpact{
		{Path: "same/work_test.go", Version: "before"},
		{Path: "same/work_test.go", Version: "after"},
		{Path: "same/work_test.go", Version: "after"},
		{Path: "new/consumer.go", Version: "after"},
		{Path: "new/consumer.go", Version: "after"},
	}}
	want := map[string]map[cg.Tag]int{
		"old":  {cg.SourceTag: 1},
		"new":  {cg.DocumentationTag: 1, cg.SourceTag: 1, cg.UnknownTag: 1},
		"same": {cg.SourceTag: 4, cg.TestTag: 1, cg.DocumentationTag: 1, cg.GitlinkTag: 1, cg.UnknownTag: 1},
	}
	groups, _ := componentResults(before, after, c, result, nil)
	if len(groups) != len(want) {
		t.Fatal(groups)
	}
	for _, group := range groups {
		if !reflect.DeepEqual(group.AffectedFileCounts, want[group.Root]) {
			t.Fatalf("%s: got %v want %v", group.Root, group.AffectedFileCounts, want[group.Root])
		}
	}
}
