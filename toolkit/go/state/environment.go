package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	depotdata "github.com/compforge/repokit/toolkit/go/internal/depot"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// releaseJournal lives outside disposable contents and is shared with Python.
type releaseJournal map[string][2]uint64

func pendingReleases(ctx context.Context, root string, recover bool) (releaseJournal, string, error) {
	info, err := Checkout(ctx, root)
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(info.GitDir, "repokit-releases.json")
	journal := releaseJournal{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	if err = json.Unmarshal(data, &journal); err != nil {
		return nil, path, err
	}
	originalCount := len(journal)
	for relative, identity := range journal {
		if !safeRelative(relative) {
			return nil, path, fmt.Errorf("unsafe release journal path: %q", relative)
		}
		stat, err := os.Lstat(filepath.Join(root, relative))
		if errors.Is(err, os.ErrNotExist) {
			delete(journal, relative)
			continue
		}
		if err != nil {
			return nil, path, err
		}
		if !stat.IsDir() || directoryIdentity(stat) != identity {
			return nil, path, fmt.Errorf("environment identity changed during release: %s", relative)
		}
	}
	// +spec=`A completed deletion must be retired before the environment can be prepared again.`
	if recover && len(journal) != originalCount {
		if err := writeReleaseJournal(path, journal); err != nil {
			return nil, path, err
		}
		slog.Info("retired completed environment releases", "checkout", root, "count", originalCount-len(journal))
	}
	return journal, path, nil
}

func writeReleaseJournal(path string, journal releaseJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".repokit-releases-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

// +spec=`Deletion retains its ownership journal until completion and refuses replaced directories.`
func releaseEnvironment(ctx context.Context, root, directory string) error {
	journal, path, err := pendingReleases(ctx, root, true)
	if err != nil {
		return err
	}
	stat, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return fmt.Errorf("environment is not a directory: %s", directory)
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil {
		return err
	}
	journal[relative] = directoryIdentity(stat)
	if err = writeReleaseJournal(path, journal); err != nil {
		return err
	}
	// Stop between entries, retaining the lease until all in-flight deletion has stopped.
	if err = removeEnvironmentContents(ctx, directory); err != nil {
		return err
	}
	delete(journal, relative)
	return writeReleaseJournal(path, journal)
}

func removeEnvironmentContents(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stat, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if stat.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err = removeEnvironmentContents(ctx, filepath.Join(path, entry.Name())); err != nil {
				return err
			}
		}
	}
	return os.Remove(path)
}

func worktreeEnvironments(ctx context.Context, root string, policy WorktreePolicy, recover bool) ([]string, bool, error) {
	resources, err := depotdata.Resources(ctx, root)
	if err != nil {
		return nil, false, err
	}
	pending, _, err := pendingReleases(ctx, root, recover)
	if err != nil {
		return nil, false, err
	}
	managed := map[string]bool{}
	unmanaged := map[string]bool{}
	for relative := range pending {
		managed[filepath.Join(root, relative)] = true
	}
	for _, resource := range resources {
		if resource.Managed {
			managed[resource.Path] = true
		} else {
			unmanaged[resource.Path] = true
		}
	}
	for _, relative := range policy.Outputs {
		directory := filepath.Join(root, relative)
		if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, false, err
		}
		managed[directory] = true
	}
	directories := make([]string, 0, len(managed))
	for directory := range managed {
		physical, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return nil, false, err
		}
		stat, err := os.Lstat(directory)
		if err != nil {
			return nil, false, err
		}
		if physical != directory || !stat.IsDir() || !withinDirectory(root, directory) {
			return nil, false, fmt.Errorf("unsafe environment path: %s", directory)
		}
		relative, _ := filepath.Rel(root, directory)
		tracked, err := worktreeQuery(ctx, root, "--literal-pathspecs", "ls-files", "-z", "--", relative)
		if err != nil {
			return nil, false, err
		}
		if tracked != "" {
			return nil, false, fmt.Errorf("environment contains tracked files: %s", relative)
		}
		if _, err = worktreeQuery(ctx, root, "check-ignore", "--quiet", "--", relative); err != nil {
			return nil, false, fmt.Errorf("environment is not ignored: %s: %w", relative, err)
		}
		err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Name() == ".git" {
				return fmt.Errorf("environment contains nested repository: %s", path)
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		directories = append(directories, directory)
		delete(unmanaged, directory)
	}
	slices.Sort(directories)
	// Coalesce declared parent outputs so a nested receipt is not deleted twice.
	roots := directories[:0]
	for _, path := range directories {
		if len(roots) == 0 || !strings.HasPrefix(path, roots[len(roots)-1]+string(filepath.Separator)) {
			roots = append(roots, path)
		}
	}
	return roots, len(unmanaged) > 0, nil
}
