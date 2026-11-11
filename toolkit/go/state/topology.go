package state

import (
	"context"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

type CheckoutInfo = gitworktree.CheckoutInfo
type CheckoutEntry = gitworktree.CheckoutEntry
type WorktreeEntry = gitworktree.WorktreeEntry

// Checkout reads current checkout topology. Persisted observations do not authorize mutations.
func Checkout(ctx context.Context, repo string) (CheckoutInfo, error) {
	return gitworktree.Checkout(ctx, repo)
}

// ListCheckouts retains registrations whose checkout location is unknown.
func ListCheckouts(ctx context.Context, repo string) ([]CheckoutEntry, error) {
	return gitworktree.ListCheckouts(ctx, repo)
}

// ListWorktrees returns current Git registrations, including bare metadata entries.
func ListWorktrees(ctx context.Context, repo string) ([]WorktreeEntry, error) {
	return gitworktree.ListWorktrees(ctx, repo)
}
