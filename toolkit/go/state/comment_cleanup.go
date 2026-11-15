package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/compforge/quality-harness/sdks/go/common"
)

// cleanComments considers only registered PR directories, and never deletes remote data.
// +spec=Only a live confirmed merged PR permits lifecycle reclamation; missing credentials and read failures retain comments. Active writers and dry-run use the same protection.
// +link=component://docs/home.md#清理与回收
func (report *CleanReport) cleanComments(ctx context.Context, directory string, options CleanOptions) error {
	if options.MergedPR == nil {
		return nil
	}
	activity, err := readActivity(directory)
	if err != nil {
		return err
	}
	if activity.LastAccessAt < float64(report.InactiveBefore.UnixNano())/1e9 {
		return nil
	}
	root := filepath.Join(directory, "prs")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("PR cache must be a real directory")
	}
	var repository common.Repository
	found, err := Read(filepath.Join(directory, "repository.json"), &repository)
	if err != nil {
		return err
	}
	if !found || repository.Forge.Name == "" || repository.Path == "" || RepositoryKey(repository.Forge.Name, repository.Path) != filepath.Base(directory) {
		return errors.New("invalid cached repository identity")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		number, err := strconv.Atoi(entry.Name())
		if err != nil || number < 1 || strconv.Itoa(number) != entry.Name() || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(root, entry.Name(), "comments")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		action := WorktreeAction{Action: "remove_pr_comments", Status: "skipped", Reason: "pr_not_merged", Path: path}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			action.Reason = "not_directory"
		} else if _, err := os.Lstat(path + ".lock"); err == nil {
			action.Reason = "state_locked"
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else {
			err = WithLock(ctx, path, func(owned func() error) error {
				reason, err := stateProtection(ctx, path)
				if err != nil {
					return err
				}
				if reason != "" {
					action.Reason = reason
					return nil
				}
				merged, err := options.MergedPR(ctx, repository, number)
				if err != nil {
					return err
				}
				if !merged {
					return nil
				}
				action.Reason = "pr_merged"
				action.EstimatedBytes, err = directoryBytes(ctx, path)
				if err != nil {
					return err
				}
				action.Status = "planned"
				if options.DryRun {
					return nil
				}
				if err := owned(); err != nil {
					return err
				}
				boundary, err := os.OpenRoot(directory)
				if err != nil {
					return err
				}
				removeErr := boundary.RemoveAll(filepath.Join("prs", entry.Name(), "comments"))
				if err := errors.Join(removeErr, boundary.Close()); err != nil {
					return err
				}
				action.Status = "completed"
				return nil
			})
			if err != nil {
				action.Status = "failed"
				action.Reason = err.Error()
				report.Errors = append(report.Errors, fmt.Sprintf("%s PR %d comments: %v", filepath.Base(directory), number, err))
			}
		}
		report.appendAction(ctx, filepath.Base(directory), action)
	}
	return nil
}
