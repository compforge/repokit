// Package worktree composes checkout lifecycle and optional dependency preparation.
package worktree

import (
	"context"
	"errors"
	"github.com/compforge/repokit/toolkit/go/depot"
	"github.com/compforge/repokit/toolkit/go/errs"
	gitworktree "github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"github.com/compforge/repokit/toolkit/go/state"
	"log/slog"
	"path/filepath"
	"strings"
)

type EnsureOptions struct {
	state.EnsureWorktreeOptions
	Prepare bool
}
type EnsureReport struct {
	state.EnsureWorktreeReport
	Name             string        `json:"name"`
	Preparation      *depot.Report `json:"preparation,omitempty"`
	PreparationError *errs.Error   `json:"preparationError,omitempty"`
	Ready            bool          `json:"ready"`
}

// Ensure preserves confirmed checkout effects when preparation fails. Ready requires
// evidence for every declared dependency Bin, not merely a successful installer call.
// +spec=Named worktrees default to the primary checkout .worktrees directory; unknown primary locations never fall back to the current worktree.
// +link=component://docs/worktrees.md
func Ensure(ctx context.Context, repo, name string, options EnsureOptions) (report EnsureReport, err error) {
	report.Name = name
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || filepath.IsAbs(name) {
		return report, &errs.Error{Code: "worktree.invalid_argument", Message: "repository and a single directory name are required"}
	}

	info, e := gitworktree.Checkout(ctx, repo)
	if e != nil {
		return report, e
	}
	primary := info.MainRoot
	if !info.Linked {
		primary = info.Root
	}
	if primary == "" {
		return report, &errs.Error{Code: "worktree.primary_unknown", Message: "primary checkout cannot be located", Hint: "Select the primary checkout with --repo."}
	}
	destination := filepath.Join(primary, ".worktrees", name)
	slog.DebugContext(ctx, "Worktree destination selected", "repository", repo, "name", name, "path", destination)
	report.EnsureWorktreeReport, err = state.EnsureWorktree(ctx, repo, destination, options.EnsureWorktreeOptions)
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
