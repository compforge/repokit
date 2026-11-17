package repokit_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go"
)

func TestDiffTagsUseChangePathAndRetainOpaqueChanges(t *testing.T) {
	root := fixture(t)
	put(t, root, "old.pb.go", "package main\nfunc OldGeneratedFunctionWithUniqueName(){}\n")
	put(t, root, "fixtures/remove.txt", "old")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "tagged fixtures")
	git(t, root, "mv", "old.pb.go", "plain.go")
	git(t, root, "rm", "fixtures/remove.txt")
	put(t, root, "fixtures/binary.bin", "a\x00b")
	put(t, root, "dist/large.min.js", strings.Repeat("x", (2<<20)+1))
	put(t, root, "kitex_gen/new.go", "package service\nfunc Generated(){}\n")
	d, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Changes) != 5 {
		t.Fatalf("lost changes: %d", len(d.Changes))
	}
	for _, ch := range d.Changes {
		switch ch.Path() {
		case "plain.go":
			if !ch.IsRenamed || !reflect.DeepEqual(ch.Tags, []cg.Tag{cg.SourceTag}) {
				t.Fatalf("rename classification: %+v", ch)
			}
		case "fixtures/remove.txt":
			if !ch.IsDeleted || !reflect.DeepEqual(ch.Tags, []cg.Tag{cg.DocumentationTag, cg.TestFixtureTag}) {
				t.Fatalf("delete classification: %+v", ch)
			}
		case "fixtures/binary.bin":
			if !ch.IsNew || !ch.IsBinary || !slices.Contains(ch.Tags, cg.TestFixtureTag) {
				t.Fatalf("binary classification: %+v", ch)
			}
		case "dist/large.min.js":
			if !ch.NewContentMissing || !reflect.DeepEqual(ch.Tags, []cg.Tag{cg.BuildOutputTag, cg.MinifiedTag, cg.SourceTag}) {
				t.Fatal("opaque path lost tags")
			}
		case "kitex_gen/new.go":
			if !reflect.DeepEqual(ch.Tags, []cg.Tag{cg.GeneratedTag, cg.SourceTag}) {
				t.Fatal(ch.Tags)
			}
		default:
			t.Fatal(ch.Path())
		}
	}
	selected := d.Select("kitex_gen/new.go")
	// Formation consumes the captured tags even if the live path disappears.
	if err := os.Remove(filepath.Join(root, "kitex_gen/new.go")); err != nil {
		t.Fatal(err)
	}
	units, err := repokit.FormUnits(context.Background(), selected, repokit.UnitOptions{})
	if err != nil || len(units.Units) == 0 || len(units.Changes) != 1 || !slices.Contains(units.Changes[0].Tags, cg.GeneratedTag) {
		t.Fatal(units, err)
	}
}

func TestDiffCustomTagsAndValidation(t *testing.T) {
	root := fixture(t)
	put(t, root, "custom.pb.go", "broken source remains a pure diff")
	for _, rules := range [][]cg.TagRule{{}, {{Name: "custom", Pattern: `\.go$`}, {Name: "custom", Pattern: `custom`}}} {
		d, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: root, TagRules: rules})
		if err != nil || !d.Complete || len(d.Changes) != 1 {
			t.Fatal(d, err)
		}
		tags := d.Changes[0].Tags
		if len(rules) == 0 && !reflect.DeepEqual(tags, []cg.Tag{cg.SourceTag}) || len(rules) > 0 && !reflect.DeepEqual(tags, []cg.Tag{"custom", cg.SourceTag}) {
			t.Fatal(tags)
		}
	}
	for _, rule := range []cg.TagRule{{Name: "custom", Pattern: "["}, {Name: " ", Pattern: ".*"}} {
		_, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: filepath.Join(root, "absent"), TagRules: []cg.TagRule{rule}})
		if err == nil || !strings.Contains(err.Error(), "tag rule") {
			t.Fatal("rule must fail before repository I/O", err)
		}
	}
}

func TestGraphPreservesDirectoryAndTags(t *testing.T) {
	root := fixture(t)
	put(t, root, "kitex_gen/service.pb.go", "package service\nfunc Work(){}\n")
	g, err := repokit.Graph(context.Background(), repokit.InputRequest{Repository: root}, 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]cg.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	doc := byID[cg.DocumentID("kitex_gen/service.pb.go")]
	dir := byID[cg.DirectoryID("kitex_gen")]
	if !slices.Contains(doc.Tags, cg.GeneratedTag) || !slices.Contains(dir.Tags, cg.GeneratedTag) || dir.Location != nil || dir.Path != "kitex_gen" {
		t.Fatal(doc, dir)
	}
	manifest := byID[cg.DocumentID("go.mod")]
	if !slices.Contains(manifest.Tags, cg.ManifestTag) || manifest.Manifest == nil {
		t.Fatal(manifest)
	}
	found := false
	for _, r := range g.Relations {
		found = found || r.Kind == cg.InDirectory && r.Source == doc.ID && r.Target == dir.ID
	}
	if !found {
		t.Fatal("missing directory relationship")
	}
	if _, ok := g.Sources[dir.Path]; ok {
		t.Fatal("directory invented source")
	}
}

func TestDirectoryDoesNotMergeIndependentUnits(t *testing.T) {
	root := fixture(t)
	put(t, root, "src/a.ts", "export function Alpha(){}\n")
	put(t, root, "src/b.ts", "export function Beta(){}\n")
	d, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	r, err := repokit.FormUnits(context.Background(), d, repokit.UnitOptions{MaxUnits: 1})
	if err != nil || len(r.Units) != 2 || !r.LimitExceeded {
		t.Fatal("physical directory merged independent modules", r, err)
	}
}

func TestFormUnitsExcludesChangesBeforeSplitting(t *testing.T) {
	root := fixture(t)
	put(t, root, "normal.go", "package main\nfunc Work() {}\n")
	put(t, root, "broken.pb.go", "not valid Go source")
	d, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: root})
	if err != nil || len(d.Changes) != 2 {
		t.Fatal(d, err)
	}
	calls := 0
	r, err := repokit.FormUnits(context.Background(), d, repokit.UnitOptions{
		Exclude: func(ch repokit.Change) bool {
			calls++
			return slices.Contains(ch.Tags, cg.GeneratedTag)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(r.Changes) != 1 || r.Changes[0].Path() != "normal.go" || len(r.Units) == 0 {
		t.Fatalf("calls=%d report=%+v", calls, r)
	}
	for _, f := range r.Fragments {
		if f.Path != "normal.go" {
			t.Fatalf("excluded fragment: %+v", f)
		}
	}
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Path == "broken.pb.go" {
			t.Fatalf("excluded file was analyzed: %+v", diagnostic)
		}
	}
	if len(d.Changes) != 2 {
		t.Fatal("mutated original diff")
	}
	if content, err := d.ReadSource(false, "broken.pb.go"); err != nil || content != "not valid Go source" {
		t.Fatal("lost captured context", content, err)
	}
	empty, err := repokit.FormUnits(context.Background(), d, repokit.UnitOptions{Exclude: func(repokit.Change) bool { return true }})
	if err != nil || len(empty.Changes) != 0 || len(empty.Fragments) != 0 || len(empty.Units) != 0 {
		t.Fatal("empty selection restored excluded files", empty, err)
	}
}

// +case:id=diff_generated_artifact_tags,desc=`Diff consumes upstream generated-output naming rules`,expect=`Source maps and protobuf outputs retain generated tags while handwritten declarations and ambiguous maps do not`
func TestDiffUsesCompleteDocumentTags(t *testing.T) {
	root := fixture(t)
	want := map[string][]cg.Tag{
		"VERSION":                           {cg.VersionTag},
		"notes.md":                          {cg.DocumentationTag},
		"asset.mystery":                     {cg.UnknownTag},
		"work_test.go":                      {cg.SourceTag, cg.TestTag},
		"devloop/dist/hooks/runtime.js.map": {cg.BuildOutputTag, cg.GeneratedTag},
		"runtime.d.ts.map":                  {cg.GeneratedTag},
		"service.pb.gw.go":                  {cg.GeneratedTag, cg.SourceTag},
		"service_pb2.py":                    {cg.GeneratedTag, cg.SourceTag},
		"service.pb.cc":                     {cg.GeneratedTag, cg.SourceTag},
		"runtime.d.ts":                      {cg.SourceTag},
		"world.map":                         {cg.UnknownTag},
	}
	for name := range want {
		put(t, root, name, "new content")
	}
	report, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != len(want) {
		t.Fatal(report.Changes)
	}
	for _, change := range report.Changes {
		if !slices.Equal(change.Tags, want[change.Path()]) {
			t.Fatalf("%s: %v", change.Path(), change.Tags)
		}
	}
}

// +case=Diff classifies symlink material from its own snapshot while retaining link text as content.
func TestDiffSymlinkTags(t *testing.T) {
	root := fixture(t)
	put(t, root, "target.py", "pass\n")
	if err := os.Symlink("target.py", filepath.Join(root, "alias.go")); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []bool{false, true} {
		if deleted {
			git(t, root, "add", ".")
			git(t, root, "commit", "-qm", "link fixture")
			if err := os.Remove(filepath.Join(root, "alias.go")); err != nil {
				t.Fatal(err)
			}
		}
		report, err := repokit.Diff(context.Background(), repokit.DiffRequest{Repository: root})
		if err != nil {
			t.Fatal(err)
		}
		selected := report.Select("alias.go")
		if len(selected.Changes) != 1 {
			t.Fatal(selected.Changes)
		}
		change := selected.Changes[0]
		if !slices.Equal(change.Tags, []cg.Tag{cg.SymlinkTag}) || change.IsDeleted != deleted {
			t.Fatal(change)
		}
	}
}
