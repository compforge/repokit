package worktree

import (
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/internal/repostate"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// +case=component://docs/state.md
func TestRepositoryStateLifecycle(t *testing.T) {
	root, git := worktreeFixture(t)
	ctx := context.Background()
	state := t.TempDir()
	git("remote", "add", "origin", "https://github.com/owner/project.git")
	path := filepath.Join(t.TempDir(), "topic")
	if result := AddWorktree(ctx, root, path, "HEAD", AddWorktreeOptions{Branch: "feature/a", StateDir: state}); result.Err != nil {
		t.Fatal(result.Err)
	}
	cached, err := LoadBranch(ctx, root, "feature/a", state, time.Hour)
	if err != nil || cached == nil || cached.Source != "cache" || len(cached.Checkouts) != 1 {
		t.Fatalf("%+v %v", cached, err)
	}
	directory := filepath.Join(state, "repositories", "github--owner--project")
	marker := filepath.Join(directory, "history", "keep")
	os.MkdirAll(filepath.Dir(marker), 0700)
	os.WriteFile(marker, []byte("history"), 0600)
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	key := repostate.Digest(path)
	recordPath := filepath.Join(directory, "checkouts", key, "observation.json")
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if repaired, err := LoadBranch(ctx, root, "feature/a", state, time.Hour); err != nil || repaired == nil || repaired.Source != "git" || len(repaired.Checkouts) != 1 {
		t.Fatalf("partial publication not repaired: %+v %v", repaired, err)
	}
	if _, err := Observe(ctx, root, state, false); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Join(directory, "branches", "feature%2Fa", "worktrees", key, "path")); err != nil || resolved != path {
		t.Fatalf("link %s %v", resolved, err)
	}
	if result := RemoveWorktree(ctx, root, path, RemoveWorktreeOptions{StateDir: state}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := os.Stat(filepath.Join(directory, "checkouts", key)); !os.IsNotExist(err) {
		t.Fatalf("record not reclaimed: %v", err)
	}
	current, err := LoadBranch(ctx, root, "feature/a", state, 0)
	if err != nil || current == nil || len(current.Checkouts) != 0 {
		t.Fatalf("%+v %v", current, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("history lost", err)
	}
	git("branch", "-D", "feature/a")
	if _, err := Observe(ctx, root, state, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "branches", "feature%2Fa")); !os.IsNotExist(err) {
		t.Fatal("branch projection not retired", err)
	}
}
func TestRepositoryStateClonesAndIsolation(t *testing.T) {
	a, gitA := worktreeFixture(t)
	b, gitB := worktreeFixture(t)
	ctx := context.Background()
	state := t.TempDir()
	for _, g := range []func(...string) string{gitA, gitB} {
		g("remote", "add", "origin", "git@github.com:owner/project.git")
		g("branch", "topic")
	}
	gitB("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "second")
	gitB("branch", "-f", "topic", "HEAD")
	headA := strings.TrimSpace(gitA("rev-parse", "topic"))
	headB := strings.TrimSpace(gitB("rev-parse", "topic"))
	if _, err := Observe(ctx, a, state, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Observe(ctx, b, state, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(a, ".git"))
	t.Setenv("GIT_WORK_TREE", a)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(a, ".git", "index"))
	for root, want := range map[string]string{a: headA, b: headB} {
		row, err := LoadBranch(ctx, root, "topic", state, time.Hour)
		if err != nil || row.Head != want {
			t.Fatalf("%+v %v", row, err)
		}
	}
	directory := filepath.Join(state, "repositories", "github--owner--project")
	data, err := os.ReadFile(filepath.Join(directory, "branches", "topic", "observation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record branchRecord
	if err := json.Unmarshal(data, &record); err != nil || len(record.Clones) != 2 {
		t.Fatalf("%s %v", data, err)
	}
	if _, err := Observe(ctx, filepath.Join(b, "missing"), state, true); err == nil {
		t.Fatal("failed read treated as absence")
	}
	after, _ := os.ReadFile(filepath.Join(directory, "branches", "topic", "observation.json"))
	if string(data) != string(after) {
		t.Fatal("failed read changed observations")
	}
}

func TestBranchReadRepairsPartialPublicationWithoutOtherBranches(t *testing.T) {
	repo, git := worktreeFixture(t)
	git("branch", "other")
	branch := strings.TrimSpace(git("symbolic-ref", "--short", "HEAD"))
	state := t.TempDir()
	ctx := context.Background()
	directory, err := Observe(ctx, repo, state, false)
	if err != nil {
		t.Fatal(err)
	}
	records, _ := filepath.Glob(filepath.Join(directory, "checkouts", "*", "observation.json"))
	if len(records) != 1 {
		t.Fatal(records)
	}
	var row checkoutObservation
	if _, err := repostate.Read(records[0], &row); err != nil {
		t.Fatal(err)
	}
	row.ObservedAt++
	if err := repostate.Write(records[0], row, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(directory, "branches", "other", "observation.json")
	if err := os.WriteFile(unrelated, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{time.Hour, 0} {
		got, err := LoadBranch(ctx, repo, branch, state, age)
		if err != nil || got == nil || got.Source != "git" || len(got.Checkouts) != 1 {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if data, _ := os.ReadFile(unrelated); string(data) != "{" {
		t.Fatal("unrelated state changed")
	}
}
