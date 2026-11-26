package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ageActivity(t *testing.T, repo, home string, old time.Time, recent ...string) string {
	t.Helper()
	directory, _, _, err := stateLocation(context.Background(), repo, home)
	if err != nil {
		t.Fatal(err)
	}
	record, err := readActivity(directory)
	if err != nil {
		t.Fatal(err)
	}
	record.LastAccessAt = float64(old.UnixNano()) / 1e9
	for key, v := range record.Checkouts {
		v.LastAccessAt = record.LastAccessAt
		record.Checkouts[key] = v
	}
	for _, path := range recent {
		v := record.Checkouts[Digest(path)]
		v.LastAccessAt = float64(time.Now().UnixNano()) / 1e9
		record.Checkouts[Digest(path)] = v
		record.LastAccessAt = v.LastAccessAt
	}
	if err = Write(filepath.Join(directory, "access.json"), record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, v := range record.Checkouts {
		if err := Write(filepath.Join(directory, "clones", Digest(v.CommonDir), "checkouts", Digest(v.Path), "access.json"), map[string]any{"version": 1, "lastAccessAt": v.LastAccessAt}, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}
func oldFiles(t *testing.T, directory string, old time.Time) {
	t.Helper()
	err := filepath.WalkDir(directory, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			return os.Chtimes(path, old, old)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestAccessRecordsOnlySelectedCheckoutAndMaintenanceDoesNotRenew(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, create := retentionFixture(t)
	linked := create("idle", false)
	if _, err := Observe(ctx, root, home, false); err != nil {
		t.Fatal(err)
	}
	directory := ageActivity(t, root, home, time.Now().Add(-40*24*time.Hour))
	before, _ := readActivity(directory)
	if _, err := Observe(ctx, root, home, false); err != nil {
		t.Fatal(err)
	}
	after, _ := readActivity(directory)
	if after.Checkouts[Digest(linked)].LastAccessAt != before.Checkouts[Digest(linked)].LastAccessAt || after.Checkouts[Digest(root)].LastAccessAt <= before.Checkouts[Digest(root)].LastAccessAt {
		t.Fatal("inventory renewed another checkout", after)
	}
	report, err := CleanWorktrees(ctx, root, WorktreeCleanOptions{Home: home, DryRun: true})
	if err != nil || len(report.Errors) > 0 {
		t.Fatal(report, err)
	}
	unchanged, _ := readActivity(directory)
	if !reflect.DeepEqual(after, unchanged) {
		t.Fatal("cleanup renewed activity")
	}
}

// +case=Global cleanup uses the same retention policy and protections as direct worktree cleanup.
func TestGlobalCleanupReusesWorktreePolicyAndProtection(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, create := retentionFixture(t)
	old := create("old", true)
	dirty := create("dirty", true)
	locked := create("locked", true)
	unknown := create("unknown", false)
	recent := create("recent", false)
	current := create("current", false)
	setCleanupPolicy(t, root, WorktreePolicy{MaxCount: 2, MaxPrepared: 0})
	unknownInfo, err := Checkout(ctx, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(unknownInfo.GitDir, "repokit-created.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "notes"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Checkout(ctx, locked)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(info.GitDir, "locked"), []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = RecordAccess(ctx, root, home); err != nil {
		t.Fatal(err)
	}
	directory := ageActivity(t, root, home, time.Now().Add(-40*24*time.Hour), recent)
	record, _ := readActivity(directory)
	delete(record.Checkouts, Digest(unknown))
	if err = Write(filepath.Join(directory, "access.json"), record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		report, err := Clean(ctx, CleanOptions{Home: home, InactiveDays: 30, DryRun: dry, CurrentDirectory: current})
		if err != nil || len(report.Errors) > 0 {
			t.Fatal(report, err)
		}
		found := false
		for _, a := range report.Actions {
			if a.Path == old && a.Action == "remove_worktree" {
				found = true
				if a.EstimatedBytes == 0 {
					t.Fatal("missing byte estimate")
				}
			}
		}
		if !found {
			t.Fatal(report)
		}
		if dry {
			if _, err = os.Stat(old); err != nil {
				t.Fatal("dry run removed source")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(unknown, "metadata.json")); !os.IsNotExist(err) {
		t.Fatal("unknown checkout retained", err)
	}
	if _, err = os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("idle checkout remains", err)
	}
	for _, path := range []string{root, locked, recent, current} {
		if _, err = os.Stat(path); err != nil {
			t.Fatal("protected source removed", path, err)
		}
	}
	if _, err = os.Stat(dirty); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dirty checkout retained", err)
	}
	if _, err = os.Stat(filepath.Join(locked, "node_modules")); err != nil {
		t.Fatal("pinned environment removed", err)
	}
	if _, err := worktreeQuery(ctx, root, "rev-parse", "refs/heads/old"); err != nil {
		t.Fatal("branch removed", err)
	}
}
func writeExpiredRepository(t *testing.T, home, key string) string {
	t.Helper()
	path := filepath.Join(home, "repositories", key)
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := Write(filepath.Join(path, "access.json"), activityRecord{Version: 1, LastAccessAt: float64(old.Unix()), Checkouts: map[string]checkoutAccess{}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "metadata.json"), []byte("cached bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	oldFiles(t, path, old)
	return path
}
func TestCleanRepositoryStateTreatsMissingActivityAsOldAndPreservesExternalSymlinks(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	os.WriteFile(marker, []byte("outside"), 0600)
	expired := writeExpiredRepository(t, home, "expired")
	os.Symlink(outside, filepath.Join(expired, "source"))
	os.Symlink(outside, filepath.Join(home, "repositories", "external"))
	unknown := filepath.Join(home, "repositories", "unknown")
	os.MkdirAll(unknown, 0700)
	os.WriteFile(filepath.Join(unknown, "metadata.json"), []byte("cached bytes"), 0600)
	refreshed := writeExpiredRepository(t, home, "refreshed")
	os.WriteFile(filepath.Join(refreshed, "new-observation"), []byte("new"), 0600)
	broken := writeExpiredRepository(t, home, "broken")
	os.WriteFile(filepath.Join(broken, "access.json"), []byte("broken"), 0600)
	before, _ := os.ReadFile(filepath.Join(expired, "access.json"))
	for _, dry := range []bool{true, false} {
		report, err := Clean(context.Background(), CleanOptions{Home: home, InactiveDays: 30, DryRun: dry})
		if err != nil || len(report.Errors) != 1 || report.EstimatedBytes == 0 {
			t.Fatal(report, err)
		}
		if dry {
			after, _ := os.ReadFile(filepath.Join(expired, "access.json"))
			if string(before) != string(after) {
				t.Fatal("preview changed activity")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(expired, "metadata.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired state retained", err)
	}
	if _, err := os.Stat(filepath.Join(unknown, "metadata.json")); !os.IsNotExist(err) {
		t.Fatal("unknown activity was not treated as old", err)
	}
	if _, err := os.Stat(filepath.Join(refreshed, "metadata.json")); !os.IsNotExist(err) {
		t.Fatal("cache mtime incorrectly renewed access", err)
	}
	for _, p := range []string{marker, broken} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal("protected data removed", p, err)
		}
	}
}
func TestGlobalCleanReclaimsWorktreeByPolicyAndExpiredStateButNeverPrimary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	ctx := context.Background()
	root, create := retentionFixture(t)
	linked := create("idle", true)
	setCleanupPolicy(t, root, WorktreePolicy{MaxCount: 0, MaxPrepared: 0})
	if err := RecordAccess(ctx, root, home); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	directory := ageActivity(t, root, home, old)
	oldFiles(t, directory, old)
	for _, dry := range []bool{true, false} {
		report, err := Clean(ctx, CleanOptions{Home: home, InactiveDays: 30, DryRun: dry})
		if err != nil || len(report.Errors) != 0 {
			t.Fatal(report, err)
		}
		removed := false
		for _, a := range report.Actions {
			removed = removed || a.Action == "remove_branch_cache"
		}
		if !removed {
			t.Fatal(report)
		}
	}
	for _, p := range []string{filepath.Join(directory, "checkouts"), linked} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("idle resource retained", p, err)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("primary source removed", err)
	}
}
func TestActivityLeaseSerializesRefreshAndCleanup(t *testing.T) {
	home := t.TempDir()
	path := writeExpiredRepository(t, home, "idle")
	ctx := context.Background()
	err := withActivity(ctx, path, func(_ context.Context, _ *activityLease) error {
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		err := RecordRepositoryAccess(short, home, "idle")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("refresh bypassed cleanup lease: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordRepositoryAccess(ctx, home, "idle"); err != nil {
		t.Fatal(err)
	}
	report, err := Clean(ctx, CleanOptions{Home: home, InactiveDays: 30})
	if err != nil || len(report.Errors) > 0 {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("refreshed state removed", err)
	}
}
func TestInvalidActivityAndCancellationNeverAuthorizeDeletion(t *testing.T) {
	home := t.TempDir()
	path := writeExpiredRepository(t, home, "idle")
	var raw map[string]any
	data, _ := os.ReadFile(filepath.Join(path, "access.json"))
	json.Unmarshal(data, &raw)
	raw["version"] = 2
	data, _ = json.Marshal(raw)
	os.WriteFile(filepath.Join(path, "access.json"), data, 0600)
	report, err := Clean(context.Background(), CleanOptions{Home: home, InactiveDays: 30})
	if err != nil || len(report.Errors) != 1 {
		t.Fatal(report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Clean(ctx, CleanOptions{Home: home, InactiveDays: 30}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("invalid state deleted", err)
	}
}

// +case=Concurrent creation releases the Git lease before waiting to publish activity, so cleanup cannot deadlock it.
func TestCreationPublicationDoesNotInvertCleanupLocks(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root, _ := worktreeFixture(t)
	if err := RecordAccess(ctx, root, home); err != nil {
		t.Fatal(err)
	}
	directory, info, _, err := stateLocation(ctx, root, home)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	done := make(chan GitResult, 1)
	err = withActivity(ctx, directory, func(_ context.Context, _ *activityLease) error {
		go func() { done <- AddWorktree(ctx, root, linked, "HEAD", AddWorktreeOptions{Branch: "new", Home: home}) }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(linked, ".git")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("checkout creation did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		budget, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		release, err := repositoryLease(budget, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
		if err != nil {
			return err
		}
		release()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not resume")
	}
}

func TestGlobalCleanDiscoversWorktreesWithoutActivity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, create := retentionFixture(t)
	linked := create("no-access", false)
	setCleanupPolicy(t, root, WorktreePolicy{MaxCount: 0, MaxPrepared: 0})
	directory, _, _, err := stateLocation(context.Background(), root, home)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(directory, "access.json")); err != nil {
		t.Fatal(err)
	}
	oldFiles(t, directory, time.Now().Add(-40*24*time.Hour))
	for _, dry := range []bool{true, false} {
		report, err := Clean(context.Background(), CleanOptions{Home: home, InactiveDays: 30, DryRun: dry})
		if err != nil || len(report.Errors) > 0 {
			t.Fatal(report, err)
		}
		found := false
		for _, a := range report.Actions {
			if a.Path == linked && a.Action == "remove_worktree" {
				found = true
				if a.Reason != "max_count" {
					t.Fatal(a)
				}
			}
		}
		if !found {
			t.Fatal(report)
		}
	}
	if _, err = os.Stat(linked); !os.IsNotExist(err) {
		t.Fatal("unknown worktree retained", err)
	}
	if _, err = os.Stat(root); err != nil {
		t.Fatal("primary deleted", err)
	}
}

func setCleanupPolicy(t *testing.T, root string, policy WorktreePolicy) {
	t.Helper()
	if err := SaveWorktreePolicy(context.Background(), root, "", policy); err != nil {
		t.Fatal(err)
	}
}

// +case=Old access does not bypass the existing count limits, and aggregate dry-run makes the same worktree decisions as the direct service.
func TestGlobalAndDirectWorktreeRetentionAgree(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, create := retentionFixture(t)
	paths := []string{create("first", true), create("second", true), create("unknown-creation", true)}
	info, err := Checkout(ctx, paths[2])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(info.GitDir, "repokit-created.json")); err != nil {
		t.Fatal(err)
	}
	directory := ageActivity(t, root, home, time.Now().Add(-40*24*time.Hour))
	oldFiles(t, directory, time.Now().Add(-40*24*time.Hour))
	for _, configured := range []bool{false, true} {
		if configured {
			setCleanupPolicy(t, root, WorktreePolicy{MaxCount: 2, MaxPrepared: 1})
		}
		direct, err := CleanWorktrees(ctx, root, WorktreeCleanOptions{Home: home, DryRun: true})
		if err != nil || len(direct.Errors) > 0 {
			t.Fatal(direct, err)
		}
		global, err := Clean(ctx, CleanOptions{Home: home, DryRun: true, InactiveDays: 30})
		if err != nil || len(global.Errors) > 0 {
			t.Fatal(global, err)
		}
		actions := []WorktreeAction{}
		for _, a := range global.Actions {
			if withinDirectory(home, a.Path) {
				continue
			}
			row := a.WorktreeAction
			row.LastAccessAt = 0
			if row.DurationSeconds <= 0 {
				t.Fatal("global retention action missing duration", row)
			}
			row.DurationSeconds = 0
			actions = append(actions, row)
		}
		for i, row := range direct.Actions {
			if row.DurationSeconds <= 0 {
				t.Fatal("direct retention action missing duration", row)
			}
			direct.Actions[i].DurationSeconds = 0
		}
		if !reflect.DeepEqual(direct.Actions, actions) {
			t.Fatal(direct.Actions, actions)
		}
		if !configured && (direct.RemainingCount != 3 || len(actions) != 0) {
			t.Fatal("age bypassed default policy", global)
		}
		if configured && (direct.RemainingCount != 2 || direct.RemainingPrepared != nil || len(actions) != 2 || actions[0].Path != paths[2] || actions[1].Path != paths[0]) {
			t.Fatal(global, direct)
		}
	}
}

// +case=Cache expiry is independent of retained worktrees independently of Forge.
func TestExpiredCacheRemovedDespiteRetainedWorktree(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, create := retentionFixture(t)
	linked := create("retained", false)
	old := time.Now().Add(-40 * 24 * time.Hour)
	directory := ageActivity(t, root, home, old)
	oldFiles(t, directory, old)
	for _, dry := range []bool{true, false} {
		report, err := Clean(ctx, CleanOptions{Home: home, InactiveDays: 30, DryRun: dry})
		if err != nil || len(report.Errors) != 0 {
			t.Fatal(report, err)
		}
		removed := false
		for _, a := range report.Actions {
			removed = removed || a.Action == "remove_branch_cache"
		}
		if !removed {
			t.Fatal(report)
		}
		if _, err := os.Stat(linked); err != nil {
			t.Fatal("retained checkout deleted", err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "checkouts")); !os.IsNotExist(err) {
		t.Fatal("expired cache retained", err)
	}
	if _, err := worktreeQuery(ctx, root, "rev-parse", "refs/heads/retained"); err != nil {
		t.Fatal(err)
	}
}

type maintenanceEventHandler struct {
	slog.Handler
	observe func(context.Context, slog.Record)
}

func (h maintenanceEventHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "worktree maintenance" {
		h.observe(ctx, r)
	}
	return h.Handler.Handle(ctx, r)
}
func onWorktreeAction(t *testing.T, observe func(context.Context, slog.Record)) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(maintenanceEventHandler{slog.NewTextHandler(io.Discard, nil), observe}))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// +case=Worktree maintenance runs outside the activity lease. Cache cleanup observes concurrent access and publication locks.
func TestCleanupRechecksAccessAndWritersAfterMaintenance(t *testing.T) {
	for _, writer := range []bool{false, true} {
		t.Run(fmt.Sprint(writer), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("REPOKIT_HOME", home)
			root, create := retentionFixture(t)
			linked := create("locked", false)
			fixtureGit(t, root, "worktree", "lock", linked)
			old := time.Now().Add(-40 * 24 * time.Hour)
			directory := ageActivity(t, root, home, old)
			oldFiles(t, directory, old)
			onWorktreeAction(t, func(ctx context.Context, _ slog.Record) {
				if ctx.Value(activityLeaseKey{}) != nil {
					t.Fatal("worktree maintenance holds activity lease")
				}
				if writer {
					if err := os.MkdirAll(LockPath(filepath.Join(directory, "topology"))+".lock", 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					// Foreground access originates outside the maintenance context.
					budget, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := RecordRepositoryAccess(budget, home, filepath.Base(directory)); err != nil {
						t.Fatal(err)
					}
				}
			})
			report, err := Clean(context.Background(), CleanOptions{Home: home, InactiveDays: 30})
			if err != nil || len(report.Errors) > 0 {
				t.Fatal(report, err)
			}
			reason := "recently_accessed"
			if writer {
				reason = "state_locked"
			}
			for _, a := range report.Actions {
				if a.Reason == reason {
					return
				}
			}
			t.Fatal(report)
		})
	}
}

func TestGlobalCleanupRetainsRegistrationForLaterReclamation(t *testing.T) {
	for _, scenario := range []string{"locked", "topology_only"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("REPOKIT_HOME", home)
			root, create := retentionFixture(t)
			linked := create("retained", false)
			setCleanupPolicy(t, root, WorktreePolicy{MaxCount: 0, MaxPrepared: 0})
			directory := ageActivity(t, root, home, time.Now().Add(-40*24*time.Hour))
			activityPath := filepath.Join(directory, "access.json")
			if scenario == "topology_only" {
				if err := os.Remove(activityPath); err != nil {
					t.Fatal(err)
				}
			}
			before, err := readActivity(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := Write(filepath.Join(directory, "validation", "history", "run.json"), map[string]string{"result": "expired"}, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			linkedInfo, err := Checkout(ctx, linked)
			if err != nil {
				t.Fatal(err)
			}
			note := filepath.Join(linkedInfo.GitDir, "locked")
			if err := os.WriteFile(note, []byte("pinned"), 0600); err != nil {
				t.Fatal(err)
			}
			options := CleanOptions{Home: home, InactiveDays: 30}
			var cacheBytes int64
			paths, err := filepath.Glob(filepath.Join(directory, "clones", "*", "branches", "*", "branch.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				size, err := directoryBytes(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				cacheBytes += size
			}

			for _, dry := range []bool{true, false} {
				options.DryRun = dry
				report, err := Clean(ctx, options)
				if err != nil || len(report.Errors) != 0 || report.EstimatedBytes != cacheBytes || cacheBytes == 0 {
					t.Fatal(report, err, cacheBytes)
				}
				if _, err := os.Stat(linked); err != nil {
					t.Fatal("protected checkout removed", err)
				}
				after, err := readActivity(directory)
				if err != nil || after.LastAccessAt != before.LastAccessAt {
					t.Fatal("maintenance renewed access", after, err)
				}
				if dry {
					if !reflect.DeepEqual(before, after) {
						t.Fatal("preview changed registration", before, after)
					}
					if scenario == "topology_only" {
						if _, err := os.Stat(activityPath); !os.IsNotExist(err) {
							t.Fatal("preview created registration", err)
						}
					}
				}
			}
			if has, err := hasRepositoryCache(directory); err != nil || has {
				t.Fatal("cache not reclaimed independently of registration", has, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "validation", "history", "run.json")); err != nil {
				t.Fatal("history lost", err)
			}

			registration, err := readActivity(directory)
			if err != nil || registration.Checkouts[Digest(root)].Path != root || registration.Checkouts[Digest(linked)].Path != linked {
				t.Fatal("checkout registration lost", registration, err)
			}
			for key, access := range registration.Checkouts {
				if access.LastAccessAt != before.Checkouts[key].LastAccessAt {
					t.Fatal("checkout access renewed", access)
				}
			}
			if err := os.Remove(note); err != nil {
				t.Fatal(err)
			}
			report, err := Clean(ctx, options)
			if err != nil || len(report.Errors) != 0 {
				t.Fatal(report, err)
			}
			if _, err := os.Stat(linked); !os.IsNotExist(err) {
				t.Fatal("later global clean lost the retained checkout", err)
			}
			if _, err := os.Stat(root); err != nil {
				t.Fatal("primary checkout removed", err)
			}
			if _, err := worktreeQuery(ctx, root, "rev-parse", "refs/heads/retained"); err != nil {
				t.Fatal("branch removed", err)
			}
			report, err = Clean(ctx, options)
			if err != nil || len(report.Errors) != 0 || len(report.Actions) != 0 || report.EstimatedBytes != 0 {
				t.Fatal("registration-only cleanup did not converge", report, err)
			}
		})
	}
}

// +case=Cancellation after a planned or completed removal preserves it and leaves later checkouts untouched.
func TestCleanWorktreesPreservesActionsOnCancellation(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("REPOKIT_HOME", home)
			root, create := retentionFixture(t)
			first := create("first", false)
			later := create("later", false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			onWorktreeAction(t, func(context.Context, slog.Record) { cancel() })
			report, err := CleanWorktrees(ctx, root, WorktreeCleanOptions{Home: home, Policy: &WorktreePolicy{}, DryRun: dry})
			if !errors.Is(err, context.Canceled) || len(report.Errors) > 0 || len(report.Actions) != 1 || report.Actions[0].Path != first {
				t.Fatal(report, err)
			}
			if _, err := os.Stat(first); dry && err != nil || !dry && !os.IsNotExist(err) {
				t.Fatal(dry, err)
			}
			if _, err := os.Stat(later); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// +case=Caller cancellation during one repository stops the sweep rather than receiving a fresh budget for the next repository.
func TestCleanCallerCancellationStopsSweep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, create := retentionFixture(t)
	linked := create("locked", false)
	fixtureGit(t, root, "worktree", "lock", linked)
	if _, err := Observe(context.Background(), root, home, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "repositories", "zz-untouched"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	onWorktreeAction(t, func(context.Context, slog.Record) { cancel() })
	report, err := Clean(ctx, CleanOptions{Home: home, InactiveDays: 30, DryRun: true})
	if !errors.Is(err, context.Canceled) || len(report.Errors) != 1 {
		t.Fatal(report, err)
	}
	for _, a := range report.Actions {
		if a.Repository == "zz-untouched" {
			t.Fatal("continued after caller cancellation", report)
		}
	}
}

// +case=A slow checkout cannot consume another checkout's processing budget.
func TestWorktreeTimeoutContinuesWithinRepository(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprint(global), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("REPOKIT_HOME", home)
			root, create := retentionFixture(t)
			slow := create("slow", false)
			fast := create("fast", false)
			setCleanupPolicy(t, root, WorktreePolicy{})
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			t.Setenv("RETENTION_SLOW", slow)
			t.Setenv("RETENTION_GIT", realGit)
			script := `#!/bin/sh
if [ "$2" = "$RETENTION_SLOW" ]; then sleep 3; fi
exec "$RETENTION_GIT" "$@"
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var issues []string
			if global {
				report, err := Clean(context.Background(), CleanOptions{Home: home, InactiveDays: 30, WorktreeTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				issues = report.Errors
			} else {
				report, err := CleanWorktrees(context.Background(), root, WorktreeCleanOptions{Home: home, Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				issues = report.Errors
			}
			if len(issues) != 1 || !strings.Contains(issues[0], slow) {
				t.Fatal(issues)
			}
			if _, err := os.Stat(slow); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(fast); !os.IsNotExist(err) {
				t.Fatal("later checkout retained", err)
			}
		})
	}
}
