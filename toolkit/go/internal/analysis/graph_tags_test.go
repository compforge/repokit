package analysis

import (
	"context"
	"os/exec"
	"testing"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/git"
)

// +case:id=graph_tag_admission,desc=`Shared directory tags exclude material before the document limit`,expect=`Replacement rules take effect without changing the captured snapshot`
func TestGraphUsesSharedTagRules(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("init fixture: %v: %s", err, out)
	}
	captured := &CapturedSnapshot{
		Report: SnapshotReport{Checkout: root, Snapshot: "captured", FileCount: 4},
		Contents: git.Snapshot{Files: map[string][]byte{
			"go.mod":                        []byte("module example.org/app\ngo 1.26\n"),
			"main.go":                       []byte("package main\nfunc main() {}\n"),
			"custom_tools/nested/broken.go": []byte("invalid source"),
			".idea/notes.txt":               []byte("ordinary material under replacement rules"),
		}},
	}
	g, err := captured.Graph(context.Background(), 3, shared.Options{
		TagRules: []shared.TagRule{{Name: shared.ToolingTag, Pattern: `^custom_tools$`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Documents != 3 || len(g.Sources) != 3 || g.Snapshot.Snapshot != "captured" || len(captured.Contents.Files) != 4 {
		t.Fatal("tag admission changed snapshot or document accounting", g)
	}
	if _, ok := g.Sources["custom_tools/nested/broken.go"]; ok {
		t.Fatal("ancestor tag did not exclude descendant")
	}
	if _, ok := g.Sources[".idea/notes.txt"]; !ok {
		t.Fatal("hardcoded tooling path survived replacement rules")
	}
	for _, d := range g.Diagnostics {
		if d.Code == "document_limit" || d.Location.Path == "custom_tools/nested/broken.go" {
			t.Fatal("excluded document consumed parsing or budget", d)
		}
	}
}
