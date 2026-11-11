package state

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func retentionFixture(t *testing.T) (string, func(string, bool) string) {
	root, git := worktreeFixture(t)
	for name, data := range map[string]string{".gitignore": "node_modules/\n.private/\n", "package.json": "{\"name\":\"fixture\"}", "package-lock.json": "{}"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	create := func(name string, prepared bool) string {
		path := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-"+name)
		t.Cleanup(func() { _ = os.RemoveAll(path) })
		if result := AddWorktree(context.Background(), root, path, "HEAD", AddWorktreeOptions{Branch: name}); result.Err != nil {
			t.Fatal(result.Err)
		}
		if prepared {
			dir := filepath.Join(path, "node_modules")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".repokit-dependencies.json"), []byte(`{"manager":"npm","fingerprint":"owned"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	return root, create
}
func TestRetentionProjectsBothLimitsAndMissingDatesOldest(t *testing.T) {
	root, create := retentionFixture(t)
	newest := create("new", true)
	old := create("old", true)
	middle := create("middle", true)
	info, err := Checkout(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(info.GitDir, "repokit-created.json")); err != nil {
		t.Fatal(err)
	}
	// Middle was created after new; give it an explicit older recorded date.
	info, err = Checkout(context.Background(), middle)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(info.GitDir, "repokit-created.json"), []byte(`{"version":1,"created_at":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	policy := WorktreePolicy{MaxCount: 2, MaxPrepared: 1}
	for _, dry := range []bool{true, false} {
		report, err := CleanWorktrees(context.Background(), root, WorktreeCleanOptions{Policy: &policy, DryRun: dry})
		if err != nil || len(report.Errors) > 0 || report.RemainingCount != 2 || report.RemainingPrepared == nil || *report.RemainingPrepared != 1 {
			t.Fatalf("%+v %v", report, err)
		}
		if len(report.Actions) != 2 || report.Actions[0].Path != old || report.Actions[1].Path != middle {
			t.Fatal(report.Actions)
		}
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old checkout retained", err)
	}
	if _, err := os.Stat(filepath.Join(newest, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(middle, "node_modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("environment retained", err)
	}
	result := gitworktree.Run(context.Background(), root, time.Second, "rev-parse", "old")
	if result.Err != nil {
		t.Fatal("branch deleted", result.Err)
	}
}
func TestDirtyCurrentAndLockedRetention(t *testing.T) {
	root, create := retentionFixture(t)
	dirty := create("dirty", true)
	locked := create("locked", true)
	if err := os.WriteFile(filepath.Join(dirty, "notes"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := gitworktree.Run(context.Background(), root, time.Second, "worktree", "lock", locked); result.Err != nil {
		t.Fatal(result.Err)
	}
	policy := WorktreePolicy{MaxCount: 0, MaxPrepared: 0}
	report, err := CleanWorktrees(context.Background(), root, WorktreeCleanOptions{Policy: &policy})
	if err != nil || len(report.Errors) > 0 || report.RemainingCount != 2 {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(filepath.Join(dirty, "notes")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dirty, "node_modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(locked, "node_modules")); err != nil {
		t.Fatal(err)
	}
	report, err = CleanWorktrees(context.Background(), dirty, WorktreeCleanOptions{Policy: &policy})
	if err != nil || len(report.Errors) > 0 || report.RemainingCount != 2 {
		t.Fatal(report, err)
	}
}
func TestInterruptedEnvironmentOwnershipSurvivesMissingReceipt(t *testing.T) {
	root, create := retentionFixture(t)
	path := create("partial", true)
	directory := filepath.Join(path, "node_modules")
	info, err := Checkout(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(releaseJournal{"node_modules": directoryIdentity(stat)})
	if err = os.WriteFile(filepath.Join(info.GitDir, "repokit-releases.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(directory, ".repokit-dependencies.json")); err != nil {
		t.Fatal(err)
	}
	policy := WorktreePolicy{MaxCount: 1, MaxPrepared: 0}
	report, err := CleanWorktrees(context.Background(), root, WorktreeCleanOptions{Policy: &policy})
	if err != nil || len(report.Errors) > 0 || *report.RemainingPrepared != 0 {
		t.Fatal(report, err)
	}
	if _, err = os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("release not resumed", err)
	}
}
func TestCreateWaitCancellationReturnsKnownFailure(t *testing.T) {
	root, _ := worktreeFixture(t)
	info, err := Checkout(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := repositoryLease(context.Background(), filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := AddWorktree(ctx, root, filepath.Join(root, "blocked"), "HEAD", AddWorktreeOptions{Branch: "blocked"})
	if result.Err == nil || result.Uncertain {
		t.Fatalf("%+v", result)
	}
}

func TestCompletedReleaseJournalRecoveryPreservesDryRun(t *testing.T) {
	root, create := retentionFixture(t)
	path := create("completed", true)
	directory := filepath.Join(path, "node_modules")
	info, err := Checkout(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(info.GitDir, "repokit-releases.json")
	original := releaseJournal{"node_modules": directoryIdentity(stat)}
	if err := writeReleaseJournal(journalPath, original); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	policy := WorktreePolicy{MaxCount: 1, MaxPrepared: 1}
	for _, dry := range []bool{true, false} {
		report, err := CleanWorktrees(context.Background(), root, WorktreeCleanOptions{Policy: &policy, DryRun: dry})
		if err != nil || len(report.Errors) > 0 {
			t.Fatal(report, err)
		}
		data, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		var journal releaseJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			t.Fatal(err)
		}
		if (len(journal) == 1) != dry {
			t.Fatal(dry, journal)
		}
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pendingReleases(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatal(err)
	}
}
