package depot

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/state"
)

type CleanOptions = state.BinCleanOptions
type CleanReport = state.WorktreeCleanReport

// Clean releases checkout-owned Bins through the same resource lease and journal as worktree retention.
func Clean(ctx context.Context, checkout string, options CleanOptions) (CleanReport, error) {
	return state.CleanBins(ctx, checkout, options)
}
