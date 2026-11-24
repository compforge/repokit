package process

import (
	"context"
	"github.com/compforge/repokit/toolkit/go/internal/git/process/command"
	"path/filepath"
	"strings"
	"time"
)

// AddWorktree creates a checkout without resetting or forcibly reusing branches.
func AddWorktree(ctx context.Context, repo, path, ref, branch string, detach bool) command.Result {
	args := []string{"worktree", "add"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	if detach {
		args = append(args, "--detach")
	}
	return command.Run(ctx, repo, 2*time.Minute, append(args, "--", path, ref)...)
}

func WorktreeRecords(ctx context.Context, repo string) (string, error) {
	result := command.Run(ctx, repo, 5*time.Second, "worktree", "list", "--porcelain", "-z")
	return result.Stdout, result.Err
}

func CheckoutPath(ctx context.Context, path, flag string) (string, error) {
	result := command.Run(ctx, path, 5*time.Second, "rev-parse", "--path-format=absolute", flag)
	if result.Err != nil {
		return "", result.Err
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(result.Stdout, "\n"))
}
