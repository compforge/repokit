package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorktreeCleanRepositoryDefaultsAndPartialOverrides(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "initial")
	path := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-linked")
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	git("worktree", "add", "-b", "topic", path, "HEAD")
	if err = os.WriteFile(filepath.Join(root, ".git", "repokit.json"), []byte(`{"worktree":{"max_count":0,"max_prepared":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want int
	}{{nil, 0}, {[]string{"--max-count", "1"}, 1}} {
		var out, stderr bytes.Buffer
		args := append([]string{"worktree", "clean", "--repo", root, "--dry-run", "--json"}, test.args...)
		code := Execute(context.Background(), args, nil, &out, &stderr)
		if code != 0 {
			t.Fatalf("%d %s", code, &stderr)
		}
		var report worktree.WorktreeCleanReport
		if err = json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.RemainingCount != test.want {
			t.Fatalf("repository config overridden: %+v", report)
		}
	}
}
func TestWorktreeCleanUsageAndExecutionExitCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, test := range []struct {
		args []string
		code int
	}{{[]string{"worktree", "clean", "--provider", "bitbucket"}, 2}, {[]string{"worktree", "clean", "--repo", "/missing-repokit-checkout"}, 1}, {[]string{"pr", "merge", "1"}, 2}} {
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), test.args, nil, &out, &stderr); code != test.code {
			t.Fatalf("%v: %d %s", test.args, code, &stderr)
		}
	}
}

func TestWorktreeObserveExplicitStateRoot(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	for _, args := range [][]string{{"init", "-qb", "main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "initial"}, {"remote", "add", "origin", "https://github.com/owner/project.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"worktree", "observe", "--repo", root, "--home", state, "--reconcile", "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatalf("%d %s", code, &stderr)
	}
	var result map[string]string
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["directory"] != filepath.Join(state, "repositories", "github--owner--project") {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(result["directory"], "branches", "main", "observation.json")); err != nil {
		t.Fatal(err)
	}
}
