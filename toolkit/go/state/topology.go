package state

import (
	"context"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"

	"github.com/compforge/repokit/toolkit/go/errs"
)

type CheckoutInfo = worktree.CheckoutInfo
type CheckoutEntry = worktree.CheckoutEntry
type WorktreeEntry = worktree.WorktreeEntry

// Checkout reads current checkout topology. Persisted observations do not authorize mutations.
func Checkout(ctx context.Context, repo string) (CheckoutInfo, error) {
	result, err := worktree.Checkout(ctx, repo)
	return result, errs.Wrap(err, "state.checkout_failed", "state.checkout", repo)
}

// ListCheckouts retains registrations whose checkout location is unknown.
func ListCheckouts(ctx context.Context, repo string) ([]CheckoutEntry, error) {
	result, err := worktree.ListCheckouts(ctx, repo)
	return result, errs.Wrap(err, "state.inventory_failed", "state.list_checkouts", repo)
}

// ListWorktrees returns current Git registrations, including bare metadata entries.
func ListWorktrees(ctx context.Context, repo string) ([]WorktreeEntry, error) {
	result, err := worktree.ListWorktrees(ctx, repo)
	return result, errs.Wrap(err, "state.inventory_failed", "state.list_worktrees", repo)
}
