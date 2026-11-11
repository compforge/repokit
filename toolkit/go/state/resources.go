package state

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// WithCheckoutResources excludes concurrent checkout mutation and cleanup while a consumer uses resources.
// +spec=Use the same cross-process lease as Python preparation and Go cleanup, without a hidden 30-second operation budget.
// +link=component://docs/depot.md
func WithCheckoutResources(ctx context.Context, repo string, paths []string, fn func(context.Context) error) (err error) {
	defer func() { err = errs.Wrap(err, "state.resource_lease_failed", "state.use_resources", repo) }()
	info, err := Checkout(ctx, repo)
	if err != nil {
		return err
	}
	unlock, err := repositoryLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	pending, _, err := pendingReleases(ctx, info.Root, true)
	if err != nil {
		return err
	}
	for _, name := range paths {
		if !safeRelative(name) {
			return fmt.Errorf("invalid checkout resource path %q", name)
		}
		for releasing := range pending {
			a, b := filepath.Join(info.Root, name), filepath.Join(info.Root, releasing)
			if withinDirectory(a, b) || withinDirectory(b, a) {
				return fmt.Errorf("resource release pending for %s; run worktree clean", releasing)
			}
		}
	}
	return fn(ctx)
}
