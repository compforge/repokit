package state

import (
	"context"
	"errors"
	"path/filepath"
)

type BinCleanOptions struct {
	Home    string
	DryRun  bool
	Outputs []string
}

// CleanBins independently releases the selected checkout's owned dependency/ output
// directories. Shared physical paths are coalesced and paths outside the checkout are retained.
// +spec=Preparation, use and release share the clone's resource lease. Cleanup never follows a shared external directory or removes tracked files; interrupted release remains recoverable.
// +link=component://docs/depot.md
func CleanBins(ctx context.Context, repo string, options BinCleanOptions) (WorktreeCleanReport, error) {
	report := WorktreeCleanReport{Actions: []WorktreeAction{}, Errors: []string{}, DryRun: options.DryRun}
	info, err := Checkout(ctx, repo)
	if err != nil {
		return report, err
	}
	unlock, err := repositoryLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if err != nil {
		return report, err
	}
	defer unlock()
	policy, err := LoadWorktreePolicy(ctx, repo, options.Home)
	if err != nil {
		return report, err
	}
	if options.Outputs != nil {
		policy.Outputs = options.Outputs
	}
	if err := policy.Validate(); err != nil {
		return report, err
	}
	directories, unmanaged, err := worktreeEnvironments(ctx, info.Root, policy, !options.DryRun)
	if err != nil {
		return report, err
	}
	for _, directory := range directories {
		size, err := releaseBins(ctx, info.Root, []string{directory}, options.DryRun)
		action := WorktreeAction{Action: "release_environment", Path: directory, Reason: "explicit", Status: "planned", EstimatedBytes: size}
		if err != nil {
			action.Status = "failed"
			action.Reason = err.Error()
			report.Errors = append(report.Errors, err.Error())
		} else if !options.DryRun {
			action.Status = "completed"
		}
		report.Actions = append(report.Actions, action)
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
	}
	if unmanaged {
		report.Actions = append(report.Actions, WorktreeAction{Action: "retain_environment", Status: "skipped", Reason: "unmanaged_environment", Path: info.Root})
	}
	return report, nil
}

// releaseBins runs under the caller's clone resource lease after domain eligibility checks.
func releaseBins(ctx context.Context, root string, paths []string, dryRun bool) (int64, error) {
	var total int64
	for _, path := range paths {
		if path == root || !withinDirectory(root, path) {
			return total, errors.New("invalid Bin deletion boundary")
		}
		size, err := directoryBytes(ctx, path)
		if err != nil {
			return total, err
		}
		total += size
		if !dryRun {
			if err := releaseEnvironment(ctx, root, path); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}
