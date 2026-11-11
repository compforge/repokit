package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// +spec=Local checkout registration outlives disposable repository observations and history. Save topology-only locations before removing observations; maintenance never renews access.
// +link=component://docs/cleanup.md
func reclaimRepositoryState(ctx context.Context, lease *activityLease, dryRun bool) (WorktreeAction, error) {
	directory := lease.directory
	action := WorktreeAction{Action: "remove_repository_state", Status: "planned", Reason: "inactive", Path: directory, LastAccessAt: lease.record.LastAccessAt}
	if action.LastAccessAt == 0 {
		action.Reason = "access_unknown"
	}
	if err := checkoutLocations(directory, lease.record.Checkouts); err != nil {
		// Incomplete discovery cannot authorize deletion of the only known locations.
		return action, err
	}
	registered := len(lease.record.Checkouts) > 0
	paths := []string{filepath.Base(directory)}
	if registered {
		action.Action = "remove_repository_cache"
		entries, err := os.ReadDir(directory)
		if err != nil {
			return action, err
		}
		paths = nil
		for _, entry := range entries {
			if entry.Name() != "activity.json" && entry.Name() != "repository.json" {
				paths = append(paths, filepath.Join(filepath.Base(directory), entry.Name()))
			}
		}
	}
	for _, path := range paths {
		size, err := directoryBytes(ctx, filepath.Join(filepath.Dir(directory), path))
		if err != nil {
			return action, err
		}
		action.EstimatedBytes += size
	}
	if len(paths) == 0 {
		action.Action, action.Status, action.Reason = "retain_repository", "skipped", "registration_only"
		return action, nil
	}
	if err := lease.owned(); err != nil {
		return action, err
	}
	if dryRun {
		return action, nil
	}
	if registered {
		// Persist the discovery inventory before its source observations can disappear.
		if err := Write(filepath.Join(directory, "activity.json"), lease.record, lease.owned); err != nil {
			return action, err
		}
	}
	boundary, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return action, err
	}
	for _, path := range paths {
		if err = lease.owned(); err != nil {
			break
		}
		// Root-relative deletion does not follow symlinks out of the state tree.
		if err = boundary.RemoveAll(path); err != nil {
			break
		}
	}
	if err = errors.Join(err, boundary.Close()); err != nil {
		return action, err
	}
	action.Status = "completed"
	return action, nil
}
