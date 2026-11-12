package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

// EnsureWorktreeOptions identifies a branch and its explicit creation baseline.
// Remote selects a freshly fetched remote branch; empty Remote resolves Base locally.
// Naming, session ownership and dependency preparation remain caller policy.
type EnsureWorktreeOptions struct{ Branch, Base, Remote, Home string }
type WorktreePhase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type WorktreeDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type EnsureWorktreeReport struct {
	Checkout       string               `json:"checkout"`
	Branch         string               `json:"branch"`
	HeadSHA        string               `json:"headSha,omitempty"`
	BaseCommit     string               `json:"baseCommit,omitempty"`
	Action         string               `json:"action,omitempty"`
	WriteAttempted bool                 `json:"writeAttempted"`
	Confirmed      bool                 `json:"confirmed"`
	Diagnostics    []WorktreeDiagnostic `json:"diagnostics"`
	Phases         []WorktreePhase      `json:"phases"`
}

func (r *EnsureWorktreeReport) measure(name string, start time.Time) {
	r.Phases = append(r.Phases, WorktreePhase{name, time.Since(start).Seconds()})
}
func (r *EnsureWorktreeReport) warn(code string, err error) {
	r.Diagnostics = append(r.Diagnostics, WorktreeDiagnostic{code, err.Error()})
}
func ensureConflict(message string) error {
	return &errs.Error{Code: "state.worktree_conflict", Message: message, Hint: "Inspect the path and Git worktree registrations; ensure never resets, replaces or prunes them."}
}

// EnsureWorktree creates or reuses the exact requested checkout and branch.
// +spec=Reuse requires matching registration, checkout identity and branch; new remote branches require a successful refresh. Existing work is never reset.
// +link=component://docs/worktrees.md
func EnsureWorktree(ctx context.Context, repo, path string, options EnsureWorktreeOptions) (report EnsureWorktreeReport, err error) {
	report.Branch = options.Branch
	report.Diagnostics = []WorktreeDiagnostic{}
	report.Phases = []WorktreePhase{}
	defer func() {
		err = errs.Wrap(err, "state.ensure_failed", "state.ensure_worktree", report.Checkout)
		if err != nil {
			slog.WarnContext(ctx, "Worktree ensure incomplete", "checkout", report.Checkout, "branch", report.Branch, "write_attempted", report.WriteAttempted, "error", err)
		}
	}()
	if path == "" || options.Branch == "" || options.Base == "" {
		return report, &errs.Error{Code: "state.invalid_worktree", Message: "path, branch and base are required"}
	}
	start := time.Now()
	info, err := gitworktree.Local(ctx, repo)
	if err != nil {
		report.measure("inspect", start)
		return report, err
	}
	if err = gitworktree.ValidateBranch(ctx, info.Root, options.Branch); err != nil {
		report.measure("inspect", start)
		return report, err
	}
	report.Checkout, err = worktreeDestination(createdPathFor(info.Root, path))
	report.measure("inspect", start)
	if err != nil {
		return report, err
	}
	start = time.Now()
	unlock, err := repositoryLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	report.measure("lock_wait", start)
	if err != nil {
		return report, err
	}
	// Cleanup acquires activity before Git. Publish state only after releasing the Git lease.
	err = func() error { defer unlock(); return ensureWorktreeLocked(ctx, info, options, &report) }()
	if err != nil {
		return report, err
	}
	if report.Action == "created" {
		if e := recordBranchCreation(ctx, report.Checkout, report.Branch, options.Base, report.BaseCommit, options.Remote, options.Home); e != nil {
			report.warn("state.creation_unavailable", e)
		}
	}
	start = time.Now()
	_, err = Observe(ctx, report.Checkout, options.Home, false)
	report.measure("observe", start)
	if err != nil {
		report.warn("state.observation_unavailable", err)
		slog.WarnContext(ctx, "Worktree confirmed; state publication unavailable", "checkout", report.Checkout, "error", err)
	}
	slog.InfoContext(ctx, "Worktree ensured", "checkout", report.Checkout, "branch", report.Branch, "action", report.Action, "head", report.HeadSHA)
	return report, nil
}

func ensureWorktreeLocked(ctx context.Context, info CheckoutInfo, options EnsureWorktreeOptions, report *EnsureWorktreeReport) error {
	start := time.Now()
	entries, err := gitworktree.ListWorktrees(ctx, info.Root)
	report.measure("inventory", start)
	if err != nil {
		return err
	}
	registered := false
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == report.Checkout {
			registered = true
			if entry.Branch != options.Branch {
				return ensureConflict("requested path is registered for a different branch or detached checkout")
			}
		}
		if entry.Branch == options.Branch && filepath.Clean(entry.Path) != report.Checkout {
			return ensureConflict("requested branch is checked out elsewhere: " + entry.Path)
		}
	}
	if registered {
		start = time.Now()
		err = confirmWorktree(ctx, info, report)
		report.measure("verify", start)
		if err == nil {
			report.Action = "reused"
			report.Confirmed = true
		}
		return err
	}
	if _, err = os.Lstat(report.Checkout); err == nil {
		return ensureConflict("requested path exists without a matching worktree registration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	start = time.Now()
	sha, exists, err := gitworktree.BranchCommit(ctx, info.Root, options.Branch)
	report.measure("branch", start)
	if err != nil {
		return err
	}
	branch := ""
	ref := options.Branch
	action := "resumed"
	if !exists {
		start = time.Now()
		if options.Remote != "" {
			sha, err = gitworktree.RefreshBranch(ctx, info.Root, options.Remote, options.Base)
		} else {
			sha, err = gitworktree.ResolveCommit(ctx, info.Root, options.Base)
		}
		report.measure("base", start)
		if err != nil {
			return err
		}
		branch, ref, action = options.Branch, sha, "created"
		report.BaseCommit = sha
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	start = time.Now()
	report.WriteAttempted = true
	result := gitworktree.Add(ctx, info.Root, report.Checkout, ref, branch, false)
	report.measure("create", start)
	if result.Err != nil {
		return checkoutError(result, "state.ensure_worktree", report.Checkout).Err
	}
	start = time.Now()
	err = confirmWorktree(ctx, info, report)
	report.measure("verify", start)
	if err != nil {
		return &errs.Error{Code: "state.worktree_unconfirmed", Message: err.Error(), Uncertain: true, Cause: err, Hint: "Creation ran but verification failed; inspect the requested checkout before retrying."}
	}
	report.Action, report.Confirmed = action, true
	start = time.Now()
	if err = recordCheckoutCreation(ctx, report.Checkout); err != nil {
		report.warn("state.creation_time_unavailable", err)
	}
	report.measure("record", start)
	return nil
}

func confirmWorktree(ctx context.Context, source CheckoutInfo, report *EnsureWorktreeReport) error {
	info, err := os.Lstat(report.Checkout)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ensureConflict("requested checkout is not a directory")
	}
	actual, err := gitworktree.Local(ctx, report.Checkout)
	if err != nil {
		return err
	}
	if actual.Root != report.Checkout || actual.CommonDir != source.CommonDir {
		return ensureConflict("requested checkout belongs to a different repository or path")
	}
	branch, err := gitworktree.CurrentBranch(ctx, report.Checkout)
	if err != nil {
		return err
	}
	if branch != report.Branch {
		return ensureConflict("checkout branch changed during ensure")
	}
	report.HeadSHA, err = gitworktree.ResolveCommit(ctx, report.Checkout, "HEAD")
	return err
}

// Canonicalize existing ancestors so aliases such as /tmp match Git registration paths.
// An existing leaf symlink is a conflict, not permission to adopt its target.
func worktreeDestination(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if info, e := os.Lstat(absolute); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", ensureConflict("requested checkout path is a symlink")
	}
	parent := filepath.Dir(absolute)
	suffix := []string{filepath.Base(absolute)}
	for {
		resolved, e := filepath.EvalSymlinks(parent)
		if e == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("cannot resolve checkout parent: %w", e)
		}
		suffix = append(suffix, filepath.Base(parent))
		parent = next
	}
}
