package worktree

import (
	"context"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"time"
)

// Add creates a checkout without resetting or forcibly reusing branches.
func Add(ctx context.Context, repo, path, ref, branch string, detach bool) command.Result {
	args := []string{"worktree", "add"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	if detach {
		args = append(args, "--detach")
	}
	return command.Run(ctx, repo, 2*time.Minute, append(args, "--", path, ref)...)
}
