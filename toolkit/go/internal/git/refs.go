package git

import (
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/gogit"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"strings"
)

func ValidateBranch(ctx context.Context, repo, branch string) error {
	if branch == "" || branch == "HEAD" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if gogit.ValidBranchName(branch) {
		return nil
	}
	return process.ValidateBranch(ctx, repo, branch)
}

// BranchCommit distinguishes a missing local branch from a failed observation.
func BranchCommit(ctx context.Context, repo, branch string) (string, bool, error) {
	return process.RefReader(repo).BranchCommit(ctx, branch)
}

func ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	return process.ResolveCommit(ctx, repo, ref)
}

// RefreshBranch accepts a configured remote name and a branch name, never a URL or arbitrary refspec.
func RefreshBranch(ctx context.Context, repo, remote, branch string) (string, error) {
	if err := ValidateBranch(ctx, repo, branch); err != nil {
		return "", err
	}
	return process.FetchBranch(ctx, repo, remote, branch)
}

func ValidObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
