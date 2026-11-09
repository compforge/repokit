package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/gitenv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitResult retains failed and uncertain outcomes. Cancellation after starting a
// write can leave effects behind; inspect the repository before retrying.
type GitResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Uncertain bool
	Err       error
}

// WorktreeEntry is a registered Git worktree. The first entry can be a bare or
// separate metadata directory rather than a usable checkout.
type WorktreeEntry struct {
	Path   string
	SHA    string
	Branch string // Empty for detached HEAD or bare repositories.
}

// CheckoutInfo describes Git topology, without caller-specific state placement.
type CheckoutInfo struct {
	Root      string
	GitDir    string
	CommonDir string
	MainRoot  string // Empty when shared metadata cannot identify the main checkout.
	Linked    bool
}

func worktreeGit(ctx context.Context, repo string, timeout time.Duration, args ...string) GitResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = gitenv.ForCheckout(os.Environ())
	cmd.WaitDelay = time.Second
	gitProcessGroup(cmd)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	// A failed start has no side effect. Once started, cancellation is indeterminate.
	uncertain := cmd.Process != nil && ctx.Err() != nil
	if err != nil {
		err = fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return GitResult{ExitCode: code, Stdout: out.String(), Stderr: stderr.String(), Uncertain: uncertain, Err: err}
}

// ListWorktrees reads NUL-delimited Git records; failure never means an empty inventory.
func ListWorktrees(ctx context.Context, repo string) ([]WorktreeEntry, error) {
	result := worktreeGit(ctx, repo, 5*time.Second, "worktree", "list", "--porcelain", "-z")
	if result.Err != nil {
		return nil, result.Err
	}
	var entries []WorktreeEntry
	for _, block := range strings.Split(result.Stdout, "\x00\x00") {
		var entry WorktreeEntry
		for _, field := range strings.Split(block, "\x00") {
			key, value, _ := strings.Cut(field, " ")
			switch key {
			case "worktree":
				entry.Path = value
			case "HEAD":
				entry.SHA = value
			case "branch":
				entry.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		}
		if entry.Path != "" {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// checkoutLocal resolves checkout paths without enumerating registered worktrees.
func checkoutLocal(ctx context.Context, repo string) (CheckoutInfo, error) {
	query := func(path, flag string) (string, error) {
		result := worktreeGit(ctx, path, 5*time.Second, "rev-parse", "--path-format=absolute", flag)
		if result.Err != nil {
			return "", result.Err
		}
		return filepath.EvalSymlinks(strings.TrimSuffix(result.Stdout, "\n"))
	}
	var info CheckoutInfo
	var err error
	if info.Root, err = query(repo, "--show-toplevel"); err != nil {
		return info, err
	}
	if _, err = os.Stat(filepath.Join(info.Root, ".git")); err != nil {
		return info, fmt.Errorf("not a checkout: %w", err)
	}
	if info.GitDir, err = query(repo, "--git-dir"); err != nil {
		return info, err
	}
	if info.CommonDir, err = query(repo, "--git-common-dir"); err != nil {
		return info, err
	}
	info.Linked = info.GitDir != info.CommonDir
	return info, nil
}

// Checkout observes the current checkout and shared metadata.
// Separate Git directories never invent a conventional primary checkout.
func Checkout(ctx context.Context, repo string) (CheckoutInfo, error) {
	entries, err := ListWorktrees(ctx, repo)
	if err != nil {
		return CheckoutInfo{}, err
	}
	return checkoutWithEntries(ctx, repo, entries)
}

func checkoutWithEntries(ctx context.Context, repo string, entries []WorktreeEntry) (CheckoutInfo, error) {
	info, err := checkoutLocal(ctx, repo)
	if err != nil {
		return info, err
	}
	query := func(path, flag string) (string, error) {
		result := worktreeGit(ctx, path, 5*time.Second, "rev-parse", "--path-format=absolute", flag)
		if result.Err != nil {
			return "", result.Err
		}
		return filepath.EvalSymlinks(strings.TrimSuffix(result.Stdout, "\n"))
	}
	if len(entries) > 0 {
		if candidate, err := query(entries[0].Path, "--show-toplevel"); err == nil {
			// A metadata directory may masquerade as its own work tree, but has no .git entry.
			if _, err := os.Stat(filepath.Join(candidate, ".git")); err == nil {
				if common, err := query(candidate, "--git-common-dir"); err == nil && common == info.CommonDir {
					info.MainRoot = candidate
				}
			}
		}
	}
	return info, nil
}

// CheckoutEntry is a checkout registration interpreted as a workspace location.
// Path is empty when the primary checkout cannot be located from shared metadata.
type CheckoutEntry struct {
	Path    string
	SHA     string
	Branch  string
	Primary bool
}

// ListCheckouts retains unknown locations and excludes bare metadata registrations.
func ListCheckouts(ctx context.Context, repo string) ([]CheckoutEntry, error) {
	entries, err := ListWorktrees(ctx, repo)
	if err != nil {
		return nil, err
	}
	info, err := checkoutWithEntries(ctx, repo, entries)
	if err != nil {
		return nil, err
	}
	main := info.MainRoot
	if !info.Linked {
		main = info.Root
	}
	out := make([]CheckoutEntry, 0, len(entries))
	for i, entry := range entries {
		// Bare metadata has no checkout HEAD.
		if entry.SHA == "" {
			continue
		}
		path := entry.Path
		if i == 0 {
			path = main
		}
		out = append(out, CheckoutEntry{Path: path, SHA: entry.SHA, Branch: entry.Branch, Primary: i == 0})
	}
	return out, nil
}

// AddWorktreeOptions chooses a new branch or a detached checkout. Both empty
// leaves Git's normal branch selection intact. Naming and ownership are caller policy.
type AddWorktreeOptions struct {
	Branch string
	Detach bool
	Home   string
}

// AddWorktree creates a checkout at an explicit path. It never replaces an existing branch.
func AddWorktree(ctx context.Context, repo, path, ref string, options AddWorktreeOptions) GitResult {
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
	defer unlock()
	args := []string{"worktree", "add"}
	if options.Branch != "" {
		args = append(args, "-b", options.Branch)
	}
	if options.Detach {
		args = append(args, "--detach")
	}
	result := worktreeGit(ctx, repo, 30*time.Second, append(args, "--", path, ref)...)
	if result.Err == nil {
		createdPath := path
		if !filepath.IsAbs(createdPath) {
			createdPath = filepath.Join(repo, createdPath)
		}
		info, err := Checkout(ctx, createdPath)
		if err == nil {
			data, marshalErr := json.Marshal(struct {
				Version   int   `json:"version"`
				CreatedAt int64 `json:"created_at"`
			}{1, time.Now().UnixNano()})
			err = marshalErr
			if err == nil {
				err = os.WriteFile(filepath.Join(info.GitDir, "repokit-created.json"), data, 0600)
			}
		}
		if err != nil {
			// Creation succeeded. Losing its optional timestamp must not invite a retry.
			result.Stderr += fmt.Sprintf("\ncreation time unavailable: %v", err)
		}
	}
	if result.Err == nil {
		if _, err := Observe(ctx, repo, options.Home, false); err != nil {
			result.Stderr += fmt.Sprintf("\ncheckout created; metadata unavailable: %v", err)
		}
	}
	return result
}

// RemoveWorktreeOptions requires explicit force to remove dirty or locked checkouts.
type RemoveWorktreeOptions struct {
	Force bool
	Home  string
}

// RemoveWorktree defaults to Git's protection of dirty and locked checkouts.
// It retains branch refs and does not choose which worktrees are eligible for removal.
func RemoveWorktree(ctx context.Context, repo, path string, options RemoveWorktreeOptions) GitResult {
	args := []string{"worktree", "remove"}
	if options.Force {
		args = append(args, "--force", "--force")
	}
	result := worktreeGit(ctx, repo, 30*time.Second, append(args, "--", path)...)
	if result.Err == nil {
		if _, err := Observe(ctx, repo, options.Home, true); err != nil {
			result.Stderr += fmt.Sprintf("\ncheckout removed; metadata unavailable: %v", err)
		}
	}
	return result
}
