// Package worktree composes checkout lifecycle and optional dependency preparation.
package worktree

import (
	"context"
	"errors"
	"github.com/compforge/repokit/toolkit/go/depot"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/state"
	"log/slog"
)

type EnsureOptions struct {
	state.EnsureWorktreeOptions
	Prepare bool
}
type EnsureReport struct {
	state.EnsureWorktreeReport
	Preparation      *depot.Report `json:"preparation,omitempty"`
	PreparationError *errs.Error   `json:"preparationError,omitempty"`
	Ready            bool          `json:"ready"`
}

// Ensure preserves confirmed checkout effects when preparation fails. Ready requires
// evidence for every declared dependency Bin, not merely a successful installer call.
// +link=component://docs/worktrees.md
func Ensure(ctx context.Context, repo, path string, options EnsureOptions) (report EnsureReport, err error) {
	report.EnsureWorktreeReport, err = state.EnsureWorktree(ctx, repo, path, options.EnsureWorktreeOptions)
	if err != nil || !report.Confirmed || !options.Prepare {
		return
	}
	preparation, prepareErr := depot.Prepare(ctx, report.Checkout, depot.Options{})
	report.Preparation = &preparation
	if prepareErr != nil {
		wrapped := errs.Wrap(prepareErr, "worktree.preparation_failed", "worktree.ensure", report.Checkout)
		errors.As(wrapped, &report.PreparationError)
	}
	report.Ready = prepareErr == nil && preparation.Complete
	for _, bin := range preparation.Bins {
		report.Ready = report.Ready && bin.Status == "ready"
	}
	slog.InfoContext(ctx, "Worktree preparation completed", "checkout", report.Checkout, "confirmed", report.Confirmed, "ready", report.Ready, "failed", prepareErr != nil)
	return report, prepareErr
}
