package state

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func worktreeFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "root")
	return root, git
}

func TestWorktreeLifecycleAndTopology(t *testing.T) {
	root, _ := worktreeFixture(t)
	ctx := context.Background()
	path := filepath.Join(root, "linked 中文\n ")
	result := AddWorktree(ctx, root, path, "HEAD", AddWorktreeOptions{Detach: true})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	entries, err := ListWorktrees(ctx, root)
	if err != nil || len(entries) != 2 || entries[1].Path != path || entries[1].Branch != "" {
		t.Fatalf("%+v %v", entries, err)
	}
	info, err := Checkout(ctx, path)
	if err != nil || !info.Linked || info.MainRoot != root || info.Root != path {
		t.Fatalf("%+v %v", info, err)
	}
	data, err := os.ReadFile(filepath.Join(info.GitDir, "repokit-created.json"))
	var created struct {
		Version   int   `json:"version"`
		Timestamp int64 `json:"created_at"`
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &created); err != nil || created.Version != 1 || created.Timestamp <= 0 {
		t.Fatalf("creation record: %s (%v)", data, err)
	}
	if err := os.WriteFile(filepath.Join(path, "dirty"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := RemoveWorktree(ctx, root, path, RemoveWorktreeOptions{}); result.Err == nil {
		t.Fatal("dirty checkout removed")
	}
	if result := RemoveWorktree(ctx, root, path, RemoveWorktreeOptions{Force: true}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkout still exists: %v", err)
	}
}

func TestSeparateGitDirectoryTopology(t *testing.T) {
	root, git := worktreeFixture(t)
	metadata := filepath.Join(t.TempDir(), "metadata")
	metadata, _ = filepath.Abs(metadata)
	git("init", "--separate-git-dir", metadata)
	metadata, _ = filepath.EvalSymlinks(metadata)
	linked := filepath.Join(root, "linked")
	ctx := context.Background()
	if result := AddWorktree(ctx, root, linked, "HEAD", AddWorktreeOptions{Detach: true}); result.Err != nil {
		t.Fatal(result.Err)
	}
	for _, path := range []string{root, linked} {
		info, err := Checkout(ctx, path)
		if err != nil || info.MainRoot != "" || info.CommonDir != metadata {
			t.Fatalf("%+v %v", info, err)
		}
	}
}

func TestWorktreeFailedObservationAndCanceledWrite(t *testing.T) {
	root, _ := worktreeFixture(t)
	if _, err := ListWorktrees(context.Background(), filepath.Join(root, "missing")); err == nil {
		t.Fatal("query failure hidden")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := AddWorktree(ctx, root, filepath.Join(root, "linked"), "HEAD", AddWorktreeOptions{Detach: true})
	if result.Err == nil || result.Uncertain {
		t.Fatalf("canceled before start: %+v", result)
	}
}
