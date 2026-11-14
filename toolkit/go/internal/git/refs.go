package git

import (
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"strings"
	"time"
)

func ValidateBranch(ctx context.Context, repo, branch string) error {
	if branch == "" || branch == "HEAD" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	return command.Run(ctx, repo, 5*time.Second, "check-ref-format", "refs/heads/"+branch).Err
}

// BranchCommit distinguishes a missing local branch from a failed observation.
func BranchCommit(ctx context.Context, repo, branch string) (string, bool, error) {
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
func ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	result := command.Run(ctx, repo, 5*time.Second, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(result.Stdout), result.Err
}
func CurrentBranch(ctx context.Context, repo string) (string, error) {
	result := command.Run(ctx, repo, 5*time.Second, "symbolic-ref", "--quiet", "HEAD")
	return strings.TrimPrefix(strings.TrimSpace(result.Stdout), "refs/heads/"), result.Err
}

// RefreshBranch accepts a configured remote name and a branch name, never a URL or arbitrary refspec.
func RefreshBranch(ctx context.Context, repo, remote, branch string) (string, error) {
	if err := ValidateBranch(ctx, repo, branch); err != nil {
		return "", err
	}
	names := command.Run(ctx, repo, 5*time.Second, "remote")
	if names.Err != nil {
		return "", names.Err
	}
	found := false
	for _, name := range strings.Split(strings.TrimSpace(names.Stdout), "\n") {
		if name == remote {
			found = true
		}
	}
	if remote == "" || strings.HasPrefix(remote, "-") || !found {
		return "", fmt.Errorf("unknown configured remote %q", remote)
	}
	ref := "refs/remotes/" + remote + "/" + branch
	fetched := command.Run(ctx, repo, 2*time.Minute, "fetch", "--no-tags", "--no-write-fetch-head", "--", remote, "+refs/heads/"+branch+":"+ref)
	if fetched.Err != nil {
		return "", fetched.Err
	}
	return ResolveCommit(ctx, repo, ref)
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
