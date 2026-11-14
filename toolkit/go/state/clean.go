package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
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
	config := struct {
		Worktree WorktreePolicy `json:"worktree"`
	}{WorktreePolicy{MaxCount: 10, MaxPrepared: 3}}
	directory, err := PersistentDirectory(ctx, repo, home)
	if err != nil {
		return config.Worktree, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return config.Worktree, nil
	}
	if err == nil {
		var fields map[string]json.RawMessage
		err = json.Unmarshal(data, &fields)
		if err == nil && fields == nil {
			err = errors.New("repository configuration must be an object")
		}
		if err == nil {
			if value, found := fields["worktree"]; found {
				var policyFields map[string]json.RawMessage
				err = json.Unmarshal(value, &policyFields)
				if err == nil && policyFields == nil {
					err = errors.New("worktree configuration must be an object")
				}
				for _, name := range []string{"max_count", "max_prepared", "outputs"} {
					if string(policyFields[name]) == "null" {
						err = fmt.Errorf("worktree %s must not be null", name)
					}
				}
			}
		}
		if err == nil {
			err = json.Unmarshal(data, &config)
		}
	}
	if err == nil {
		err = config.Worktree.Validate()
	}
	return config.Worktree, err
}

type WorktreeAction struct {
	Action         string  `json:"action"`
	Status         string  `json:"status"`
	Reason         string  `json:"reason"`
	Path           string  `json:"path"`
	Branch         string  `json:"branch"`
	PRNumber       int     `json:"pr_number,omitempty"`
	EstimatedBytes int64   `json:"estimatedBytes,omitempty"`
	LastAccessAt   float64 `json:"lastAccessAt,omitempty"`
}

type WorktreeCleanReport struct {
	Actions           []WorktreeAction `json:"actions"`
	Errors            []string         `json:"errors"`
	RemainingCount    int              `json:"remaining_count"`
	RemainingPrepared *int             `json:"remaining_prepared"`
	DryRun            bool             `json:"dry_run"`
}

// WorktreePullRequests supplies live, source-verified PR facts for a local branch.
// Both inventory and pre-removal verification use the same repository PR capability.
type WorktreePullRequests interface {
	MergedPullRequest(context.Context, string, string) (int, error)
}

type WorktreeCleanOptions struct {
	Policy         *WorktreePolicy
	DryRun         bool
	PullRequests   WorktreePullRequests
	Home           string
	ProtectedPaths []string
}

type retentionEntry struct {
	CheckoutEntry
	created      int64
	environments []string
	unmanaged    bool
	excluded     bool
	removed      bool
}

// CleanWorktrees removes merged checkouts, then old excess checkouts and environments.
//
// +spec=`Unknown creation times sort oldest. Protected old entries do not displace the newest retained set. Eligible checkouts are removed whole, including submodule clones; primary repositories and shared branch refs are retained.`
// +spec=Caller cancellation stops remaining worktrees and returns confirmed actions without reporting untouched entries as failures.
// +link=component://docs/worktrees.md
func CleanWorktrees(ctx context.Context, repo string, options WorktreeCleanOptions) (result WorktreeCleanReport, err error) {
	ctx = maintenanceContext(ctx)
	defer func() { err = errs.Wrap(err, "state.cleanup_failed", "state.cleanup", repo) }()
	report := WorktreeCleanReport{Actions: []WorktreeAction{}, Errors: []string{}, DryRun: options.DryRun}
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
	info, err := Checkout(ctx, repo)
	if err != nil {
		return report, err
	}
	unlock, err := repositoryLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if err != nil {
		return report, err
	}
	defer unlock()
	entries, err := ListCheckouts(ctx, repo)
	if err != nil {
		return report, err
	}
	var candidates []*retentionEntry
	known := true
	action := func(e *retentionEntry, kind, status, reason string, number int) {
		report.Actions = append(report.Actions, WorktreeAction{Action: kind, Status: status, Reason: reason, Path: e.Path, Branch: e.Branch, PRNumber: number})
		if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok {
			report.Actions[len(report.Actions)-1].LastAccessAt = lease.record.Checkouts[Digest(e.Path)].LastAccessAt
		}
		slog.Info("worktree maintenance", "action", kind, "status", status, "path", e.Path, "reason", reason)
	}
	failure := func(e *retentionEntry, kind string, err error) {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", e.Path, err))
		action(e, kind, "failed", err.Error(), 0)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if entry.Primary {
			continue
		}
		e := &retentionEntry{CheckoutEntry: entry}
		candidates = append(candidates, e)
		home, homeErr := ResolveHome(options.Home)
		if homeErr != nil {
			return report, homeErr
		}
		if canonical, e := filepath.EvalSymlinks(home); e == nil {
			home = canonical
		}
		if withinDirectory(e.Path, home) {
			e.excluded = true
			action(e, "retain", "skipped", "contains_state_home", 0)
			continue
		}
		for _, path := range options.ProtectedPaths {
			if withinDirectory(e.Path, path) {
				e.excluded = true
				break
			}
		}
		if e.excluded {
			known = false
			action(e, "retain", "skipped", "current_checkout", 0)
			continue
		}
		reason, observeErr := protectionInInventory(ctx, info.Root, entry, entries)
		if observeErr == nil {
			e.environments, e.unmanaged, observeErr = worktreeEnvironments(ctx, e.Path, *policy, !options.DryRun)
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if observeErr != nil {
			known = false
			e.excluded = true
			failure(e, "retain", observeErr)
			continue
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
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if observeErr != nil {
			e.excluded = true
			failure(e, "retain", observeErr)
		}
		if reason != "" {
			e.excluded = true
			action(e, "retain", "skipped", reason, 0)
		}
	}
	remove := func(e *retentionEntry, reason string, number int) {
		if e.excluded || e.Path == "" {
			return
		}
		protection, err := worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
		if err == nil && protection == "" {
			protection, err = worktreeRemovalProtection(ctx, e.Path, e.environments)
		}
		if err == nil && protection == "" {
			protection, err = worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
		}
		if err != nil {
			failure(e, "remove_worktree", err)
			return
		}
		if protection != "" {
			action(e, "remove_worktree", "skipped", protection, number)
			return
		}
		size, err := directoryBytes(ctx, e.Path)
		if err != nil {
			failure(e, "remove_worktree", err)
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
			result := RemoveWorktree(ctx, repo, e.Path, RemoveWorktreeOptions{Home: options.Home, Force: true})
			if result.Err != nil {
				failure(e, "remove_worktree", result.Err)
				return
			}
		}
		e.removed = true
		status := "completed"
		if options.DryRun {
			status = "planned"
		}
		action(e, "remove_worktree", status, reason, number)
		report.Actions[len(report.Actions)-1].EstimatedBytes = size
	}
	if options.PullRequests != nil {
		for _, e := range candidates {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if e.excluded {
				continue
			}
			number, err := options.PullRequests.MergedPullRequest(ctx, e.Branch, e.SHA)
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			if err != nil {
				e.excluded = true
				failure(e, "retain", err)
				continue
			}
			if number == 0 {
				continue
			}
			verified, err := options.PullRequests.MergedPullRequest(ctx, e.Branch, e.SHA)
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			if err != nil || verified != number {
				e.excluded = true
				if err == nil {
					err = errors.New("merged PR identity changed")
				}
				failure(e, "retain", err)
				continue
			}
			remove(e, "merged", number)
		}
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
	survivors := make([]*retentionEntry, 0, len(candidates))
	for _, e := range candidates {
		if !e.removed {
			survivors = append(survivors, e)
		}
	}
	for _, e := range survivors[:max(0, len(survivors)-policy.MaxCount)] {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		remove(e, "max_count", 0)
	}
	prepared := make([]*retentionEntry, 0, len(survivors))
	for _, e := range survivors {
		if !e.removed && (len(e.environments) > 0 || e.unmanaged) {
			prepared = append(prepared, e)
		}
	}
	for _, e := range prepared[:max(0, len(prepared)-policy.MaxPrepared)] {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if e.excluded {
			continue
		}
		reason, err := worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
		if err != nil {
			failure(e, "release_environment", err)
			continue
		}
		if reason != "" {
			action(e, "release_environment", "skipped", reason, 0)
			continue
		}
		directories, unmanaged, err := worktreeEnvironments(ctx, e.Path, *policy, !options.DryRun)
		if err == nil && !slices.Equal(directories, e.environments) {
			err = errors.New("environment inventory changed")
		}
		if err != nil {
			failure(e, "release_environment", err)
			continue
		}
		if len(directories) == 0 {
			action(e, "release_environment", "skipped", "unmanaged_environment", 0)
			continue
		}
		reason, err = worktreeProtection(ctx, repo, info.Root, e.CheckoutEntry)
		if err != nil {
			failure(e, "release_environment", err)
			continue
		}
		if reason != "" {
			action(e, "release_environment", "skipped", reason, 0)
			continue
		}
		var size int64
		for _, directory := range directories {
			n, e := directoryBytes(ctx, directory)
			if e != nil {
				err = e
				break
			}
			size += n
		}
		if err != nil {
			failure(e, "release_environment", err)
			continue
		}
		if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok {
			if err := lease.owned(); err != nil {
				failure(e, "release_environment", err)
				continue
			}
		}
		if !options.DryRun {
			for _, directory := range directories {
				if err = releaseEnvironment(ctx, e.Path, directory); err != nil {
					break
				}
			}
		}
		if err != nil {
			failure(e, "release_environment", err)
			continue
		}
		e.environments = nil
		e.unmanaged = unmanaged
		status := "completed"
		if options.DryRun {
			status = "planned"
		}
		action(e, "release_environment", status, "max_prepared", 0)
		report.Actions[len(report.Actions)-1].EstimatedBytes = size
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	countPrepared := 0
	for _, e := range candidates {
		if !e.removed {
			report.RemainingCount++
			if len(e.environments) > 0 || e.unmanaged {
				countPrepared++
			}
		}
	}
	if known {
		report.RemainingPrepared = &countPrepared
	}
	changed := false
	for _, a := range report.Actions {
		changed = changed || a.Status == "completed"
	}
	if !options.DryRun && changed {
		if _, err := Observe(ctx, repo, options.Home, true); err != nil {
			report.Errors = append(report.Errors, "repository observation: "+err.Error())
		}
	}
	return report, nil
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

func worktreeRemovalProtection(ctx context.Context, path string, directories []string) (string, error) {
	status, err := worktreeQuery(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "unsaved_changes", nil
	}
	ignored, err := worktreeQuery(ctx, path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return "", err
	}
	for _, name := range strings.Split(ignored, "\x00") {
		if name == "" {
			continue
		}
		managed := false
		for _, directory := range directories {
			managed = managed || withinDirectory(directory, filepath.Join(path, name))
		}
		if !managed {
			return "unmanaged_ignored_files", nil
		}
	}
	return "", nil
}
