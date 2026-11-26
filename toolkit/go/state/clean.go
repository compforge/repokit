package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/process/command"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// WorktreePolicy retains the newest linked checkouts and prepared environments.
type WorktreePolicy struct {
	MaxCount    int      `json:"max_count"`
	MaxPrepared int      `json:"max_prepared"`
	Outputs     []string `json:"outputs,omitempty"`
}

func (p WorktreePolicy) Validate() (err error) {
	defer func() { err = errs.Wrap(err, "state.invalid_policy", "state.policy", "") }()
	if p.MaxPrepared < 0 || p.MaxCount < p.MaxPrepared {
		return errors.New("worktree limits require 0 <= max_prepared <= max_count")
	}
	for _, path := range p.Outputs {
		if !safeRelative(path) {
			return fmt.Errorf("unsafe regenerable output: %q", path)
		}
	}
	return nil
}

func safeRelative(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	return true
}

// LoadWorktreePolicy reads configuration shared by the repository's checkouts.
func LoadWorktreePolicy(ctx context.Context, repo, home string) (policy WorktreePolicy, err error) {
	defer func() { err = errs.Wrap(err, "state.policy_failed", "state.policy", repo) }()
	policy = WorktreePolicy{MaxCount: 10, MaxPrepared: 3}
	resolved, err := ResolveHome(home)
	if err != nil {
		return policy, err
	}
	directory, err := PersistentDirectory(ctx, repo, home)
	if err != nil {
		return policy, err
	}
	for _, path := range []string{filepath.Join(resolved, "config.json"), filepath.Join(directory, "config.json")} {
		if err := overlayPolicy(path, "worktree", &policy); err != nil {
			return policy, err
		}
	}
	return policy, policy.Validate()
}

type WorktreeAction struct {
	Action          string  `json:"action"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason"`
	Path            string  `json:"path"`
	Branch          string  `json:"branch"`
	PRNumber        int     `json:"pr_number,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	EstimatedBytes  int64   `json:"estimatedBytes,omitempty"`
	LastAccessAt    float64 `json:"lastAccessAt,omitempty"`
}

func (r *WorktreeCleanReport) recordActionDuration(index int, start time.Time) {
	if index < len(r.Actions) {
		r.Actions[index].DurationSeconds = time.Since(start).Seconds()
	}
}

type WorktreeCleanReport struct {
	BeforeCount       int              `json:"before_count"`
	MaxCount          int              `json:"max_count"`
	MaxPrepared       int              `json:"max_prepared"`
	OverCount         int              `json:"over_count"`
	OverPrepared      *int             `json:"over_prepared"`
	Actions           []WorktreeAction `json:"actions"`
	Errors            []string         `json:"errors"`
	RemainingCount    int              `json:"remaining_count"`
	RemainingPrepared *int             `json:"remaining_prepared"`
	DryRun            bool             `json:"dry_run"`
}

type WorktreeCleanOptions struct {
	// Timeout is the cumulative active processing budget per worktree. Zero uses only the caller context.
	Timeout        time.Duration
	Policy         *WorktreePolicy
	DryRun         bool
	Home           string
	ProtectedPaths []string
	// SkipBusy avoids waiting for a concurrent Git lifecycle operation.
	SkipBusy bool
}

type retentionEntry struct {
	CheckoutEntry
	created   int64
	remaining time.Duration
	inspected bool
	prepared  bool
	excluded  bool
	removed   bool
}

// CleanWorktrees applies both limits to the same creation-time ranking.
//
// +spec=`Unknown creation times sort oldest. Protected old entries do not displace the newest retained set. Eligible checkouts are removed whole, including submodule clones; primary repositories and shared branch refs are retained.`
// +spec=Eligible checkouts are force-removed with dirty, untracked and ignored contents; environment ownership only gates partial environment release.
// +spec=Each worktree has an independent cumulative processing budget across planning and removal or environment release; time spent on other worktrees does not consume it.
// +spec=Caller cancellation stops remaining worktrees and returns confirmed actions without reporting untouched entries as failures.
// +link=component://docs/worktrees.md
func CleanWorktrees(ctx context.Context, repo string, options WorktreeCleanOptions) (result WorktreeCleanReport, err error) {
	ctx = maintenanceContext(ctx)
	defer func() { err = errs.Wrap(err, "state.cleanup_failed", "state.cleanup", repo) }()
	report := WorktreeCleanReport{Actions: []WorktreeAction{}, Errors: []string{}, DryRun: options.DryRun}
	if options.Timeout < 0 {
		return report, errors.New("worktree timeout must not be negative")
	}
	policy := options.Policy
	if policy == nil {
		loaded, err := LoadWorktreePolicy(ctx, repo, options.Home)
		if err != nil {
			return report, err
		}
		policy = &loaded
	}
	if err := policy.Validate(); err != nil {
		return report, err
	}
	report.MaxCount, report.MaxPrepared = policy.MaxCount, policy.MaxPrepared
	info, err := Checkout(ctx, repo)
	if err != nil {
		return report, err
	}
	acquire := repositoryLease
	if options.SkipBusy {
		acquire = repogit.TryAcquireLease
	}
	unlock, err := acquire(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if err != nil {
		return report, err
	}
	defer unlock()
	entries, err := ListCheckouts(ctx, repo)
	if err != nil {
		return report, err
	}
	home, err := ResolveHome(options.Home)
	if err != nil {
		return report, err
	}
	if canonical, err := filepath.EvalSymlinks(home); err == nil {
		home = canonical
	}
	var candidates []*retentionEntry
	action := func(e *retentionEntry, kind, status, reason string) {
		report.Actions = append(report.Actions, WorktreeAction{Action: kind, Status: status, Reason: reason, Path: e.Path, Branch: e.Branch})
		if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok {
			report.Actions[len(report.Actions)-1].LastAccessAt = lease.record.Checkouts[Digest(e.Path)].LastAccessAt
		}
		slog.InfoContext(ctx, "worktree maintenance", "action", kind, "status", status, "path", e.Path, "reason", reason)
	}
	failure := func(e *retentionEntry, kind string, err error) {
		if ctx.Err() != nil {
			return
		}
		e.excluded = true
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", e.Path, err))
		action(e, kind, "failed", err.Error())
	}
	// Each pass charges only time spent processing this checkout, not queueing behind others.
	run := func(e *retentionEntry, fn func(context.Context)) {
		workCtx := ctx
		cancel := func() {}
		started := time.Now()
		if options.Timeout > 0 {
			workCtx, cancel = context.WithTimeout(ctx, e.remaining)
		}
		defer cancel()
		fn(workCtx)
		if options.Timeout > 0 {
			e.remaining -= time.Since(started)
			if workCtx.Err() != nil && !e.excluded && !e.removed {
				failure(e, "retain", workCtx.Err())
			}
		}
	}
	// A missing directory never takes a retention slot. This also handles another
	// process finishing the deletion between discovery and Git's remove.
	retireAbsent := func(ctx context.Context, e *retentionEntry) bool {
		if e.Path == "" || ctx.Err() != nil {
			return false
		}
		if _, err := os.Lstat(e.Path); !errors.Is(err, os.ErrNotExist) {
			return false
		}
		status, reason, err := retireMissingWorktree(ctx, repo, e.CheckoutEntry, options.DryRun)
		if err != nil {
			failure(e, "remove_worktree", err)
		} else {
			action(e, "remove_worktree", status, reason)
		}
		e.removed = true
		return true
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if entry.Primary {
			continue
		}
		e := &retentionEntry{CheckoutEntry: entry, remaining: options.Timeout}
		run(e, func(ctx context.Context) {
			if retireAbsent(ctx, e) {
				return
			}
			if withinDirectory(e.Path, home) {
				e.excluded = true
				action(e, "retain", "skipped", "contains_state_home")
			} else {
				for _, path := range options.ProtectedPaths {
					if withinDirectory(e.Path, path) {
						e.excluded = true
						action(e, "retain", "skipped", "current_checkout")
						break
					}
				}
			}
			// Protected paths still participate in the newest retention window.
			// Read their creation time before deciding which older entries exceed it.
			reason, observeErr := protectionInInventory(ctx, info.Root, entry, entries)
			if observeErr != nil {
				if retireAbsent(ctx, e) {
					return
				}
				failure(e, "retain", observeErr)
				return
			}
			metadata, observeErr := worktree.Local(ctx, e.Path)
			if observeErr == nil {
				var data []byte
				data, observeErr = os.ReadFile(filepath.Join(metadata.GitDir, "repokit-created.json"))
				if errors.Is(observeErr, os.ErrNotExist) {
					observeErr = nil
				} else if observeErr == nil {
					var record struct {
						Version int   `json:"version"`
						Created int64 `json:"created_at"`
					}
					observeErr = json.Unmarshal(data, &record)
					if observeErr == nil && (record.Version != 1 || record.Created <= 0) {
						observeErr = errors.New("invalid creation record")
					}
					e.created = record.Created
				}
			}
			if observeErr != nil {
				if retireAbsent(ctx, e) {
					return
				}
				failure(e, "retain", observeErr)
			}
			if reason != "" {
				e.excluded = true
				action(e, "retain", "skipped", reason)
			}
		})
		if !e.removed {
			candidates = append(candidates, e)
			report.BeforeCount++
		}
	}
	remove := func(ctx context.Context, e *retentionEntry) {
		if e.excluded || e.Path == "" {
			return
		}
		defer report.recordActionDuration(len(report.Actions), time.Now())
		eligible := func() bool {
			reason, err := worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
			if err == nil && reason == "" {
				return true
			}
			if !retireAbsent(ctx, e) {
				if err != nil {
					failure(e, "remove_worktree", err)
				} else {
					action(e, "remove_worktree", "skipped", reason)
				}
			}
			return false
		}
		if !eligible() {
			return
		}
		size, err := directoryBytes(ctx, e.Path)
		if err != nil {
			if !retireAbsent(ctx, e) {
				failure(e, "remove_worktree", err)
			}
			return
		}
		if !eligible() {
			return
		}
		if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok {
			if err := lease.owned(); err != nil {
				failure(e, "remove_worktree", err)
				return
			}
		}
		if !options.DryRun {
			// Eligibility belongs to this service; Git removes the entire checkout
			// and its registration. One force allows submodules but preserves locks.
			result := command.Run(ctx, repo, 30*time.Second, "worktree", "remove", "--force", "--", e.Path)
			if result.Err != nil {
				if !retireAbsent(ctx, e) {
					failure(e, "remove_worktree", result.Err)
				}
				return
			}
		}
		e.removed = true
		status := "completed"
		if options.DryRun {
			status = "planned"
		}
		action(e, "remove_worktree", status, "max_count")
		report.Actions[len(report.Actions)-1].EstimatedBytes = size
	}
	slices.SortFunc(candidates, func(a, b *retentionEntry) int {
		if a.created < b.created {
			return -1
		}
		if a.created > b.created {
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	// +spec=Both windows count all linked checkouts, including unprepared and protected entries.
	removeEnd := max(0, len(candidates)-policy.MaxCount)
	releaseEnd := max(0, len(candidates)-policy.MaxPrepared)
	for _, e := range candidates[:removeEnd] {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		run(e, func(ctx context.Context) { remove(ctx, e) })
	}
	for _, e := range candidates[removeEnd:releaseEnd] {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if e.excluded {
			continue
		}
		run(e, func(ctx context.Context) {
			defer report.recordActionDuration(len(report.Actions), time.Now())
			reason, err := worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
			if err != nil {
				failure(e, "release_environment", err)
				return
			}
			if reason != "" {
				action(e, "release_environment", "skipped", reason)
				return
			}
			directories, unmanaged, err := worktreeEnvironments(ctx, e.Path, nil, !options.DryRun)
			if err != nil {
				failure(e, "inspect_environment", err)
				return
			}
			e.inspected = true
			e.prepared = unmanaged || len(directories) > 0
			if len(directories) == 0 {
				if unmanaged {
					action(e, "release_environment", "skipped", "unmanaged_environment")
				}
				return
			}
			reason, err = worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
			if err != nil {
				failure(e, "release_environment", err)
				return
			}
			if reason != "" {
				action(e, "release_environment", "skipped", reason)
				return
			}
			if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok {
				if err := lease.owned(); err != nil {
					failure(e, "release_environment", err)
					return
				}
			}
			size, err := releaseBins(ctx, e.Path, directories, options.DryRun)
			if err != nil {
				failure(e, "release_environment", err)
				return
			}
			e.prepared = unmanaged
			status := "completed"
			if options.DryRun {
				status = "planned"
			}
			action(e, "release_environment", status, "max_prepared")
			report.Actions[len(report.Actions)-1].EstimatedBytes = size
		})
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	known := true
	countPrepared := 0
	for _, e := range candidates {
		if !e.removed {
			report.RemainingCount++
			if !e.inspected || e.excluded {
				known = false
			}
			if e.prepared {
				countPrepared++
			}
		}
	}
	report.OverCount = max(0, report.RemainingCount-policy.MaxCount)
	if known {
		over := max(0, countPrepared-policy.MaxPrepared)
		report.OverPrepared = &over
		report.RemainingPrepared = &countPrepared
	}
	changed := false
	for _, a := range report.Actions {
		changed = changed || a.Status == "completed"
	}
	if !options.DryRun && !changed {
		// A previous removal may have completed in Git before publishing Home.
		directory, err := PersistentDirectory(ctx, repo, home)
		if err != nil {
			return report, err
		}
		stored, err := os.ReadDir(filepath.Join(directory, "clones", Digest(info.CommonDir), "checkouts"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
		live := map[string]bool{}
		for _, entry := range entries {
			live[Digest(entry.Path)] = true
		}
		for _, record := range stored {
			if record.IsDir() && !live[record.Name()] {
				changed = true
				break
			}
		}
	}
	if !options.DryRun && changed {
		if _, err := Observe(ctx, repo, options.Home, true); err != nil {
			report.Errors = append(report.Errors, "repository observation: "+err.Error())
		}
	}
	return report, nil
}

// retireMissingWorktree runs under the clone lifecycle lock. Recheck Git identity
// and the missing directory immediately before targeting the registration without force.
func retireMissingWorktree(ctx context.Context, repo string, entry CheckoutEntry, dry bool) (string, string, error) {
	entries, err := ListCheckouts(ctx, repo)
	if err != nil {
		return "", "", err
	}
	for _, live := range entries {
		if live.Path != entry.Path {
			continue
		}
		if live.Locked {
			return "skipped", "locked", nil
		}
		if live.Primary || !live.Prunable || live.Branch != entry.Branch || live.SHA != entry.SHA {
			return "skipped", "checkout_identity_changed", nil
		}
		if _, err := os.Lstat(live.Path); !errors.Is(err, os.ErrNotExist) {
			return "skipped", "checkout_identity_changed", err
		}
		if dry {
			return "planned", "missing_checkout", nil
		}
		if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok {
			if err := lease.owned(); err != nil {
				return "", "", err
			}
		}
		if _, err := worktreeQuery(ctx, repo, "worktree", "remove", "--", live.Path); err != nil {
			// A concurrent removal is already successful. Other Git errors remain visible.
			latest, queryErr := ListCheckouts(ctx, repo)
			if queryErr != nil || slices.ContainsFunc(latest, func(row CheckoutEntry) bool { return row.Path == live.Path }) {
				return "", "", err
			}
		}
		return "completed", "missing_checkout", nil
	}
	if dry {
		return "skipped", "already_absent", nil
	}
	return "completed", "already_absent", nil
}

func worktreeQuery(ctx context.Context, repo string, args ...string) (string, error) {
	result := command.Run(ctx, repo, 5*time.Second, args...)
	return result.Stdout, result.Err
}

func worktreeProtection(ctx context.Context, repo, current string, entry CheckoutEntry) (string, error) {
	if entry.Path == "" || entry.Primary || entry.Path == current {
		return "primary_or_current_checkout", nil
	}
	entries, err := ListCheckouts(ctx, repo)
	if err != nil {
		return "", err
	}
	return protectionInInventory(ctx, current, entry, entries)
}

// Inventory reuse is limited to planning. The mutation gate calls worktreeProtection again.
func protectionInInventory(ctx context.Context, current string, entry CheckoutEntry, entries []CheckoutEntry) (string, error) {
	if entry.Path == "" || entry.Primary || entry.Path == current {
		return "primary_or_current_checkout", nil
	}
	if !slices.Contains(entries, entry) {
		return "checkout_identity_changed", nil
	}
	if entry.Branch == "" {
		return "detached_checkout", nil
	}
	info, err := worktree.Local(ctx, entry.Path)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"locked", "index.lock", "MERGE_HEAD", "rebase-merge", "rebase-apply"} {
		_, err := os.Lstat(filepath.Join(info.GitDir, name))
		if err == nil {
			if name == "locked" {
				return "locked", nil
			}
			return "git_operation_in_progress", nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	for _, other := range entries {
		if other.Path != entry.Path && withinDirectory(entry.Path, other.Path) {
			return "nested_checkout", nil
		}
	}
	return "", nil
}

func withinDirectory(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
