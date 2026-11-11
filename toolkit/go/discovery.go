package repokit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/git"
)

// FindCheckout locates a physical ancestor checkout. Empty means absent; path,
// metadata or Git failures return errors. Bare metadata is not a checkout.
func FindCheckout(ctx context.Context, directory string) (checkout string, err error) {
	defer func() { err = errs.Wrap(err, "repository.discovery_failed", "repository.discover", directory) }()
	start, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	start, err = filepath.EvalSymlinks(start)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(start)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", directory)
	}
	for candidate := start; ; candidate = filepath.Dir(candidate) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if filepath.Base(candidate) == ".git" {
			return "", nil
		}
		if _, err := os.Lstat(filepath.Join(candidate, ".git")); err == nil {
			repo, err := git.Open(ctx, candidate)
			if err != nil {
				return "", err
			}
			if repo.Root != candidate {
				return "", fmt.Errorf("Git environment does not match discovered checkout")
			}
			return repo.Root, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(candidate) == candidate {
			return "", nil
		}
	}
}
