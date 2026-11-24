// Package process implements checkout-bound operations using native Git commands.
package process

import (
	"context"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/git/command"
)

// RefReader is the root path of the checkout to query.
type RefReader string

func (reader RefReader) BranchCommit(ctx context.Context, branch string) (string, bool, error) {
	repo := string(reader)
	ref := "refs/heads/" + branch
	result := command.Run(ctx, repo, 5*time.Second, "show-ref", "--verify", "--quiet", ref)
	if result.ExitCode == 1 && !result.Uncertain {
		return "", false, nil
	}
	if result.Err != nil {
		return "", false, result.Err
	}
	sha, err := ResolveCommit(ctx, repo, ref)
	return sha, true, err
}

// ResolveCommit resolves and peels a revision to its commit.
func ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	result := command.Run(ctx, repo, 5*time.Second, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(result.Stdout), result.Err
}

// Formats reports the object and ref formats advertised by the local Git binary.
func (reader RefReader) Formats(ctx context.Context) (string, error) {
	result := command.Run(ctx, string(reader), 5*time.Second, "rev-parse", "--show-object-format", "--show-ref-format")
	return strings.TrimSpace(result.Stdout), result.Err
}
