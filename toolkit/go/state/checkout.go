package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"os"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// GitResult retains the adapter's actual outcome, including uncertain writes.
type GitResult = command.Result

// AddWorktreeOptions chooses a new branch or a detached checkout. Both empty
// leaves Git's normal branch selection intact. Naming and ownership are caller policy.
type AddWorktreeOptions struct {
	Branch string
	Detach bool
	Home   string
}

// AddWorktree creates a checkout at an explicit path. It never replaces an existing branch.
// +spec=`State owns Git mutation and publication. A publication failure retains the successful Git outcome.`
// +link=component://docs/state.md
func AddWorktree(ctx context.Context, repo, path, ref string, options AddWorktreeOptions) (operation GitResult) {
	defer func() { operation = checkoutError(operation, "state.add_worktree", path) }()
	if options.Branch != "" && options.Detach {
		return GitResult{ExitCode: -1, Err: errors.New("branch and detach are mutually exclusive")}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := Checkout(ctx, repo)
	if err != nil {
		return GitResult{ExitCode: -1, Err: err}
	}
	unlock, err := repositoryLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if err != nil {
		return GitResult{ExitCode: -1, Err: err}
	}
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	result := worktree.Add(ctx, repo, path, ref, options.Branch, options.Detach)
	if result.Err == nil {
		if err := recordCheckoutCreation(ctx, createdPathFor(repo, path)); err != nil {
			result.Stderr += fmt.Sprintf("\ncreation time unavailable: %v", err)
		}
	}
	// Publish after releasing the Git lease: cleanup acquires activity before Git.
	unlock()
	released = true
	if result.Err == nil {
		if _, err := Observe(ctx, createdPathFor(repo, path), options.Home, false); err != nil {
			result.Stderr += fmt.Sprintf("\ncheckout created; metadata unavailable: %v", err)
		}
	}
	return result
}

// RemoveWorktreeOptions requires explicit force to remove dirty or submodule checkouts.
// Explicit Git worktree locks remain protected, including with Force.
type RemoveWorktreeOptions struct {
	Force bool
	Home  string
}

// RemoveWorktree defaults to Git's protection of dirty and locked checkouts.
// It retains branch refs and does not choose which worktrees are eligible for removal.
func RemoveWorktree(ctx context.Context, repo, path string, options RemoveWorktreeOptions) (operation GitResult) {
	defer func() { operation = checkoutError(operation, "state.remove_worktree", path) }()
	args := []string{"worktree", "remove"}
	if options.Force {
		args = append(args, "--force")
	}
	result := command.Run(ctx, repo, 30*time.Second, append(args, "--", path)...)
	if result.Err == nil {
		if _, err := Observe(ctx, repo, options.Home, true); err != nil {
			result.Stderr += fmt.Sprintf("\ncheckout removed; metadata unavailable: %v", err)
		}
	}
	return result
}

func checkoutError(result GitResult, op, path string) GitResult {
	result.Err = errs.Wrap(result.Err, "state.checkout_failed", op, path)
	if result.Uncertain && result.Err != nil {
		result.Err = &errs.Error{Code: "git.outcome_unknown", Message: result.Err.Error(), Op: op, Subject: path, Uncertain: true, Hint: "Inspect the checkout before retrying the write.", Cause: result.Err}
	}
	return result
}

func createdPathFor(repo, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repo, path)
}

func recordCheckoutCreation(ctx context.Context, path string) error {
	info, err := worktree.Local(ctx, path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Version   int   `json:"version"`
		CreatedAt int64 `json:"created_at"`
	}{1, time.Now().UnixNano()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(info.GitDir, "repokit-created.json"), data, 0600)
}
