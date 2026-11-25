package process

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/git/process/command"
)

func ValidateBranch(ctx context.Context, repo, branch string) error {
	return command.Run(ctx, repo, 5*time.Second, "check-ref-format", "refs/heads/"+branch).Err
}

// FetchBranch refreshes a validated branch from a configured remote.
func FetchBranch(ctx context.Context, repo, remote, branch string) (string, error) {
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

// MergeBase returns the unique best common ancestor for two local revisions.
func MergeBase(ctx context.Context, repo, target string) (string, error) {
	result := command.Run(ctx, repo, 10*time.Second, "merge-base", "--all", "--", "HEAD", target)
	if result.Err != nil {
		return "", result.Err
	}
	bases := strings.Fields(result.Stdout)
	if len(bases) != 1 {
		return "", fmt.Errorf("expected one merge base with %s, found %d", target, len(bases))
	}
	return bases[0], nil
}
