package state

import (
	"errors"
	"os"
	"path/filepath"
)

func hasRepositoryCache(directory string) (bool, error) {
	return remainingRepositoryCache(directory, nil)
}
func remainingRepositoryCache(directory string, removed []string) (bool, error) {
	covered := func(path string) bool {
		for _, parent := range removed {
			if path == parent || withinDirectory(parent, path) {
				return true
			}
		}
		return false
	}

	for _, name := range []string{"metadata.json", "remotes"} {
		if covered(filepath.Join(directory, name)) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	prs, err := directories(filepath.Join(directory, "prs"))
	if err != nil {
		return false, err
	}
	for _, pr := range prs {
		if covered(pr) {
			continue
		}
		entries, err := os.ReadDir(pr)
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if entry.Name() != "access.json" && !covered(filepath.Join(pr, entry.Name())) {
				return true, nil
			}
		}
	}
	clones, err := directories(filepath.Join(directory, "clones"))
	if err != nil {
		return false, err
	}
	for _, clone := range clones {
		branches, err := directories(filepath.Join(clone, "branches"))
		if err != nil {
			return false, err
		}
		for _, branch := range branches {
			for _, name := range []string{"branch.json", "associations"} {
				if covered(filepath.Join(branch, name)) {
					continue
				}
				if _, err := os.Lstat(filepath.Join(branch, name)); err == nil {
					return true, nil
				} else if !errors.Is(err, os.ErrNotExist) {
					return false, err
				}
			}
		}
	}
	return false, nil
}
