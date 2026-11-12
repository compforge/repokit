package git

import (
	"context"
	"errors"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pushFixture(t *testing.T) (string, string, PushOptions) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, remote, sha := tagFixture(t)
	command(t, repo, "branch", "-M", "topic")
	return repo, remote, PushOptions{Branch: "topic", Commit: sha, Remote: "origin"}
}

// +case=Publish only the frozen commit to the push URL; preserve the index/tree, omit unrequested tags and reuse matching remote evidence.
func TestPushDestinationAndIdempotence(t *testing.T) {
	repo, remote, opts := pushFixture(t)
	fetch := filepath.Join(t.TempDir(), "fetch.git")
	command(t, repo, "init", "--bare", "-q", fetch)
	command(t, repo, "remote", "set-url", "origin", fetch)
	command(t, repo, "remote", "set-url", "--push", "origin", remote)
	command(t, repo, "tag", "-a", "unrequested", "-m", "tag")
	command(t, repo, "config", "push.followTags", "true")
	if e := os.WriteFile(filepath.Join(repo, "dirty"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	before := command(t, repo, "status", "--porcelain")
	opts.Target = "published/topic"
	opts.SetUpstream = true
	r, e := Push(context.Background(), repo, opts)
	if e != nil || !r.Confirmed || !r.PushAttempted || r.UpstreamConfirmed || len(r.Diagnostics) != 1 || r.RemoteCommit != opts.Commit {
		t.Fatal(r, e)
	}
	if command(t, remote, "rev-parse", "refs/heads/published/topic") != opts.Commit || command(t, fetch, "for-each-ref", "--format=%(refname)") != "" || command(t, remote, "tag", "--list") != "" {
		t.Fatal("wrong publication scope")
	}
	if command(t, repo, "status", "--porcelain") != before || command(t, repo, "config", "branch.topic.remote") != "origin" || command(t, repo, "config", "branch.topic.merge") != "refs/heads/published/topic" {
		t.Fatal(r)
	}
	again, e := Push(context.Background(), repo, opts)
	if e != nil || !again.Confirmed || again.PushAttempted {
		t.Fatal(again, e)
	}
}
func TestPushFastForwardAndRejectRewrite(t *testing.T) {
	repo, remote, opts := pushFixture(t)
	if _, e := Push(context.Background(), repo, opts); e != nil {
		t.Fatal(e)
	}
	first := opts.Commit
	command(t, repo, "commit", "--allow-empty", "-qm", "second")
	opts.Commit = command(t, repo, "rev-parse", "HEAD")
	r, e := Push(context.Background(), repo, opts)
	if e != nil || !r.Confirmed {
		t.Fatal(r, e)
	}
	second := opts.Commit
	command(t, repo, "reset", "--hard", first)
	opts.Commit = first
	r, e = Push(context.Background(), repo, opts)
	requireProblem(t, e, "git.push_rejected", false)
	if r.Confirmed || r.RemoteCommit != second || command(t, remote, "rev-parse", "refs/heads/topic") != second {
		t.Fatal(r)
	}
}

// +case=Check observes the original publication even after the local branch advances; it never pushes or configures upstream.
func TestPushCheckAndSourceIdentity(t *testing.T) {
	repo, remote, opts := pushFixture(t)
	opts.Check = true
	r, e := Push(context.Background(), repo, opts)
	if e != nil || r.Confirmed || r.RemoteState != "missing" || r.PushAttempted {
		t.Fatal(r, e)
	}
	opts.Check = false
	if _, e = Push(context.Background(), repo, opts); e != nil {
		t.Fatal(e)
	}
	command(t, repo, "commit", "--allow-empty", "-qm", "new local commit")
	r, e = Push(context.Background(), repo, opts)
	requireProblem(t, e, "git.push_source_changed", false)
	if r.PushAttempted {
		t.Fatal(r)
	}
	opts.Check = true
	r, e = Push(context.Background(), repo, opts)
	if e != nil || !r.Confirmed || r.PushAttempted || command(t, remote, "rev-parse", "refs/heads/topic") != opts.Commit {
		t.Fatal(r, e)
	}
	opts.SetUpstream = true
	_, e = Push(context.Background(), repo, opts)
	requireProblem(t, e, "git.push_invalid_argument", false)
}
func TestPushInvalidInputsAndAmbiguousRemote(t *testing.T) {
	for _, input := range []PushOptions{{Branch: "topic", Commit: "HEAD"}, {Branch: "-option", Commit: strings.Repeat("a", 40)}, {Branch: "topic", Commit: strings.Repeat("a", 40), Target: "../bad"}} {
		repo, _, _ := pushFixture(t)
		r, e := Push(context.Background(), repo, input)
		requireProblem(t, e, "git.push_invalid_argument", false)
		if r.PushAttempted {
			t.Fatal(r)
		}
	}
	repo, remote, opts := pushFixture(t)
	command(t, repo, "remote", "set-url", "--add", "--push", "origin", remote)
	command(t, repo, "remote", "set-url", "--add", "--push", "origin", remote+"-other")
	r, e := Push(context.Background(), repo, opts)
	requireProblem(t, e, "git.ambiguous_remote", false)
	if r.PushAttempted {
		t.Fatal(r)
	}
}

// +case=Lost successful push responses are reconciled; failed verification stays unknown and is recovered by a read-only check.
func TestPushUnknownAndReconciliation(t *testing.T) {
	for _, scenario := range []string{"response_lost", "verify_failed", "unacknowledged"} {
		t.Run(scenario, func(t *testing.T) {
			repo, _, opts := pushFixture(t)
			pushed := false
			count := 0
			run := func(ctx context.Context, repo string, budget time.Duration, args ...string) gitworktree.GitResult {
				if args[0] == "push" {
					pushed = true
					count++
					if scenario == "unacknowledged" {
						return gitworktree.GitResult{ExitCode: -1, Err: context.DeadlineExceeded, Uncertain: true}
					}
					r := gitworktree.Run(ctx, repo, budget, args...)
					if r.Err != nil {
						t.Fatal(r.Err)
					}
					if scenario == "response_lost" {
						return gitworktree.GitResult{ExitCode: -1, Err: context.DeadlineExceeded, Uncertain: true}
					}
					return r
				}
				if args[0] == "ls-remote" && pushed && scenario == "verify_failed" {
					return gitworktree.GitResult{ExitCode: 1, Err: errors.New("offline")}
				}
				return gitworktree.Run(ctx, repo, budget, args...)
			}
			r, e := push(context.Background(), repo, opts, run)
			if count != 1 {
				t.Fatal(count)
			}
			if scenario == "response_lost" {
				if e != nil || !r.Confirmed || len(r.Diagnostics) != 1 {
					t.Fatal(r, e)
				}
			} else {
				requireProblem(t, e, "git.push_unknown", true)
				if r.Confirmed {
					t.Fatal(r)
				}
				opts.Check = true
				observed, e := Push(context.Background(), repo, opts)
				if e != nil || observed.PushAttempted || observed.Confirmed != (scenario == "verify_failed") {
					t.Fatal(observed, e)
				}
			}
		})
	}
}
func TestPushRejectsSourceChangeDuringPreflight(t *testing.T) {
	repo, _, opts := pushFixture(t)
	run := func(ctx context.Context, repo string, budget time.Duration, args ...string) gitworktree.GitResult {
		r := gitworktree.Run(ctx, repo, budget, args...)
		if args[0] == "ls-remote" {
			command(t, repo, "commit", "--allow-empty", "-qm", "concurrent")
		}
		return r
	}
	r, e := push(context.Background(), repo, opts, run)
	requireProblem(t, e, "git.push_source_changed", false)
	if r.PushAttempted {
		t.Fatal(r)
	}
}
func TestPushKeepsFrozenCommitDuringWrite(t *testing.T) {
	repo, remote, opts := pushFixture(t)
	run := func(ctx context.Context, repo string, budget time.Duration, args ...string) gitworktree.GitResult {
		if args[0] == "push" {
			command(t, repo, "commit", "--allow-empty", "-qm", "concurrent after preflight")
		}
		return gitworktree.Run(ctx, repo, budget, args...)
	}
	r, e := push(context.Background(), repo, opts, run)
	if e != nil || !r.Confirmed || command(t, remote, "rev-parse", "refs/heads/topic") != opts.Commit || command(t, repo, "rev-parse", "HEAD") == opts.Commit {
		t.Fatal(r, e)
	}
}
func TestPushUpstreamFailureRetainsRemoteSuccess(t *testing.T) {
	repo, _, opts := pushFixture(t)
	opts.SetUpstream = true
	run := func(ctx context.Context, repo string, budget time.Duration, args ...string) gitworktree.GitResult {
		if args[0] == "config" && args[1] == "--local" && args[2] == "--replace-all" {
			return gitworktree.GitResult{ExitCode: 1, Err: errors.New("config locked")}
		}
		return gitworktree.Run(ctx, repo, budget, args...)
	}
	r, e := push(context.Background(), repo, opts, run)
	if e != nil || !r.Confirmed || r.UpstreamConfirmed || len(r.Diagnostics) != 1 {
		t.Fatal(r, e)
	}
}
func TestPushRemoteFailureBeforeWrite(t *testing.T) {
	repo, _, opts := pushFixture(t)
	for _, raw := range []string{"not a ref", opts.Commit + "\trefs/heads/wrong", opts.Commit + "\trefs/heads/topic\n" + opts.Commit + "\trefs/heads/topic"} {
		run := func(ctx context.Context, repo string, budget time.Duration, args ...string) gitworktree.GitResult {
			if args[0] == "ls-remote" {
				return gitworktree.GitResult{Stdout: raw}
			}
			return gitworktree.Run(ctx, repo, budget, args...)
		}
		r, e := push(context.Background(), repo, opts, run)
		requireProblem(t, e, "git.invalid_response", false)
		if r.PushAttempted {
			t.Fatal(r)
		}
	}
}
