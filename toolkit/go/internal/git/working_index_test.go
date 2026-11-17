package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
)

// +case:id=working_index_path_kinds,desc=`The index contains unusual tracked paths, intent-to-add, a symlink and an unavailable gitlink`,expect=`Working capture preserves contents and repository boundaries while distinguishing untracked, deleted and ignored paths`
func TestWorkingSnapshotIndexPathKinds(t *testing.T) {
	root := fixture(t)
	unusual := "tracked\tline\nfile.go"
	put(t, root, unusual, "staged")
	if err := os.Symlink("intent.go", filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, root, "add", unusual, "link.go")
	put(t, root, unusual, "working")
	put(t, root, "intent.go", "intent")
	gitCommand(t, root, "add", "-N", "intent.go")
	oid := strings.TrimSpace(gitCommand(t, root, "rev-parse", "HEAD"))
	gitCommand(t, root, "update-index", "--add", "--cacheinfo", "160000,"+oid+",child")
	put(t, root, "untracked file.go", "untracked")
	put(t, root, "ignored.ts", "ignored")
	if err := os.Remove(filepath.Join(root, "source file.ts")); err != nil {
		t.Fatal(err)
	}
	repo, err := repogit.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, added, err := repo.Working(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{unusual: "working", "intent.go": "intent", "untracked file.go": "untracked"} {
		if string(snapshot.Files[name]) != want {
			t.Fatalf("%q: got %q, want %q", name, snapshot.Files[name], want)
		}
	}
	for _, name := range []string{"source file.ts", "ignored.ts", "link.go", "child"} {
		if _, ok := snapshot.Files[name]; ok {
			t.Fatalf("unexpected captured file %q", name)
		}
	}
	if snapshot.Links["link.go"] != "intent.go" || snapshot.Modules["child"] != oid {
		t.Fatalf("lost typed index entries: %+v", snapshot)
	}
	if !slices.Equal(added, []string{"untracked file.go"}) {
		t.Fatalf("untracked paths: %q", added)
	}
}

// +case:id=working_unmerged_index,desc=`A file has unresolved index stages`,expect=`Capture rejects the conflict rather than returning a partial snapshot`
func TestWorkingSnapshotRejectsUnmergedIndex(t *testing.T) {
	root := fixture(t)
	oid := strings.TrimSpace(gitCommand(t, root, "rev-parse", "HEAD:source file.ts"))
	cmd := exec.Command("git", "-C", root, "update-index", "--index-info")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("100644 %s 1\tconflict.go\n100644 %s 2\tconflict.go\n", oid, oid))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("index fixture: %v: %s", err, out)
	}
	repo, err := repogit.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Working(context.Background()); err == nil || !strings.Contains(err.Error(), "unmerged index entry: conflict.go") {
		t.Fatalf("conflict error: %v", err)
	}
}
