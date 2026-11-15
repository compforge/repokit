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
	"time"
)

type EnsureOptions struct {
	state.EnsureWorktreeOptions
	Prepare bool
	// Timeout bounds creation/preparation together, then each worktree's maintenance independently.
	// Zero uses only the caller context.
	Timeout time.Duration
	// ProtectedPaths includes the invoking checkout when it differs from repo.
	ProtectedPaths []string
}
type EnsureReport struct {
	state.EnsureWorktreeReport
	Maintenance      *state.WorktreeCleanReport `json:"maintenance,omitempty"`
	Name             string                     `json:"name"`
	Preparation      *depot.Report              `json:"preparation,omitempty"`
	PreparationError *errs.Error                `json:"preparationError,omitempty"`
	Ready            bool                       `json:"ready"`
}

// Ensure preserves confirmed checkout effects when preparation fails. Ready requires
// evidence for every declared dependency Bin, not merely a successful installer call.
// +spec=Named worktrees default to the primary checkout .worktrees directory; unknown primary locations never fall back to the current worktree.
// +spec=Confirmed ensure results trigger synchronous clone-local capacity maintenance after preparation; no PR lookup bypasses the retention window. Source and destination checkouts remain protected; maintenance failures are warnings.
// +link=component://docs/worktrees.md
func Ensure(ctx context.Context, repo, name string, options EnsureOptions) (report EnsureReport, err error) {
	report.Name = name
	if options.Timeout < 0 {
		return report, &errs.Error{Code: "worktree.invalid_argument", Message: "timeout must not be negative"}
	}
	operationCtx := ctx
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		operationCtx, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || filepath.IsAbs(name) {
		return report, &errs.Error{Code: "worktree.invalid_argument", Message: "repository and a single directory name are required"}
	}

	info, e := gitworktree.Checkout(operationCtx, repo)
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
	report.EnsureWorktreeReport, err = state.EnsureWorktree(operationCtx, repo, destination, options.EnsureWorktreeOptions)
	if report.Confirmed {
		// Creation and Depot release their leases before this deferred maintenance.
		defer func() {
			start := time.Now()
			protected := append([]string{info.Root, report.Checkout}, options.ProtectedPaths...)
			maintenance, maintenanceErr := state.CleanWorktrees(ctx, info.Root, state.WorktreeCleanOptions{
				Home: options.Home, Timeout: options.Timeout, ProtectedPaths: protected, SkipBusy: true,
			})
			report.Maintenance = &maintenance
			report.Phases = append(report.Phases, state.WorktreePhase{Name: "maintenance", DurationSeconds: time.Since(start).Seconds()})
			warnings := append([]string{}, maintenance.Errors...)
			if maintenanceErr != nil {
				warnings = append(warnings, maintenanceErr.Error())
			}
			for _, message := range warnings {
				report.Diagnostics = append(report.Diagnostics, state.WorktreeDiagnostic{Code: "worktree.maintenance_incomplete", Message: message})
				slog.WarnContext(ctx, "Worktree maintenance incomplete", "checkout", report.Checkout, "error", message)
			}
			slog.InfoContext(ctx, "Worktree capacity maintenance finished", "checkout", report.Checkout, "actions", len(maintenance.Actions), "warnings", len(warnings))
		}()
	}
	if err != nil || !report.Confirmed || !options.Prepare {
		return
	}
	preparation, prepareErr := depot.Prepare(operationCtx, report.Checkout, depot.Options{})
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
