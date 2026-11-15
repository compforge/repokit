package repokit

import (
	"context"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go/internal/validation/history"
	"github.com/compforge/repokit/toolkit/go/state"
)

type ValidationCleanupReport = history.CleanupReport

func CleanValidationHistory(ctx context.Context, base, key string, days, maxCount int, dryRun bool) (ValidationCleanupReport, error) {
	return history.Clean(ctx, base, key, days, maxCount, dryRun)
}

// Clean composes resource owners. The CLI supplies remote access, never cleanup order.
func Clean(ctx context.Context, options state.CleanOptions) (state.CleanReport, error) {
	options.Validation = func(ctx context.Context, home, key string, maximum int, dry bool) (state.ResourceCount, error) {
		result, err := history.Clean(ctx, filepath.Join(home, "repositories"), key, 30, maximum, dry)
		return state.ResourceCount{Repository: key, Resource: "validation", Before: result.Before, After: result.After, MaxCount: maximum, OverBudget: result.OverBudget}, err
	}
	return state.Clean(ctx, options)
}
