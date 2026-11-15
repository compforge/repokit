package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanLocks reclaims expired empty leases left by exited processes. Live leases
// are owned by their operation and are released even after their resource is deleted.
// +spec=Resource retention never extends a lease. Cleanup rechecks identity and renewal before removing an expired lock, and never recursively deletes lock contents.
// +link=component://docs/home.md#文件组织思路
func CleanLocks(ctx context.Context, home, key string, dryRun bool) ([]WorktreeAction, error) {
	root, err := ResolveHome(home)
	if err != nil {
		return nil, err
	}
	if key != "" && (filepath.Base(key) != key || key == "." || key == "..") {
		return nil, errors.New("invalid repository key")
	}
	actions := []WorktreeAction{}
	if info, e := os.Lstat(filepath.Join(root, "locks")); errors.Is(e, os.ErrNotExist) {
		return actions, nil
	} else if e != nil {
		return actions, e
	} else if !info.IsDir() {
		return actions, errors.New("locks must be a real directory")
	}
	for _, kind := range []string{"activity", "resources"} {
		directory := filepath.Join(root, "locks", kind)
		if info, e := os.Lstat(directory); errors.Is(e, os.ErrNotExist) {
			continue
		} else if e != nil {
			return actions, e
		} else if !info.IsDir() {
			return actions, errors.New("lock collection must be a real directory")
		}
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return actions, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return actions, err
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".lock") || (key != "" && entry.Name() != key+".lock") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			before, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return actions, err
			}
			if time.Since(before.ModTime()) <= 30*time.Second {
				continue
			}
			contents, err := os.ReadDir(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return actions, err
			}
			if len(contents) != 0 {
				continue
			}
			current, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return actions, err
			}
			if !os.SameFile(before, current) || !before.ModTime().Equal(current.ModTime()) {
				continue
			}
			action := WorktreeAction{Action: "remove_stale_lock", Status: "planned", Reason: "lease_expired", Path: path}
			if !dryRun {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return actions, err
				}
				action.Status = "completed"
			}
			actions = append(actions, action)
		}
	}
	return actions, nil
}
func publicationBusy(directory string) (bool, error) {
	info, err := os.Lstat(LockPath(filepath.Join(directory, "cleanup")) + ".lock")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return time.Since(info.ModTime()) <= 30*time.Second, nil
}
