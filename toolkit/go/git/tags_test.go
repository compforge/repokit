package git

import (
	"context"
	"errors"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func tagFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "-q", repo)
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "commit", "--allow-empty", "-qm", "first")
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, repo, "remote", "add", "origin", remote)
	return repo, remote, runGit(t, repo, "rev-parse", "HEAD")
}
func requireProblem(t *testing.T, err error, code string, uncertain bool) {
	t.Helper()
	var p *errs.Error
	if !errors.As(err, &p) || p.Code != code || p.Uncertain != uncertain {
		t.Fatalf("want %s uncertain=%t, got %v", code, uncertain, err)
	}
}

// +case=Publish only requested tags to the push URL; duplicate and annotated tags at the same commit are idempotent
func TestPublishTagsPushDestinationAndIdempotence(t *testing.T) {
	repo, remote, sha := tagFixture(t)
	fetch := filepath.Join(t.TempDir(), "fetch.git")
	runGit(t, repo, "init", "--bare", "-q", fetch)
	runGit(t, repo, "remote", "set-url", "origin", fetch)
	runGit(t, repo, "remote", "set-url", "--push", "origin", remote)
	runGit(t, repo, "tag", "-a", "local-only", "-m", "unrequested", sha)
	runGit(t, repo, "config", "push.followTags", "true")
	opts := PublishTagsOptions{Target: "HEAD", Tags: []string{"toolkit/go/v1.0.0", "candidate", "candidate"}}
	report, err := PublishTags(context.Background(), repo, opts)
	if err != nil || !report.Confirmed || !report.PushAttempted || len(report.Tags) != 2 || len(report.Phases) != 4 {
		t.Fatal(report, err)
	}
	if runGit(t, fetch, "tag", "--list") != "" || runGit(t, remote, "tag", "--list", "local-only") != "" {
		t.Fatal("pushed outside requested scope")
	}
	runGit(t, remote, "-c", "user.name=Test", "-c", "user.email=test@example.com", "tag", "-a", "annotated", "-m", "existing", sha)
	opts.Tags = append(opts.Tags, "annotated")
	report, err = PublishTags(context.Background(), repo, opts)
	if err != nil || !report.Confirmed || report.PushAttempted || report.TargetCommit != sha {
		t.Fatal(report, err)
	}
	for _, tag := range report.Tags {
		if tag.ObjectID != sha || tag.Status != "present" {
			t.Fatal(tag)
		}
	}
	if runGit(t, repo, "tag", "--list") != "local-only" {
		t.Fatal("publication changed local tags")
	}
}

// +case=One conflicting tag prevents every missing tag in the batch from being pushed
func TestPublishTagsConflictAndInputValidation(t *testing.T) {
	repo, remote, first := tagFixture(t)
	if _, err := PublishTags(context.Background(), repo, PublishTagsOptions{Target: first, Tags: []string{"existing"}}); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "commit", "--allow-empty", "-qm", "second")
	report, err := PublishTags(context.Background(), repo, PublishTagsOptions{Target: "HEAD", Tags: []string{"existing", "new"}})
	requireProblem(t, err, "git.tag_conflict", false)
	if report.PushAttempted || runGit(t, remote, "tag", "--list", "new") != "" {
		t.Fatal(report)
	}
	for _, opts := range []PublishTagsOptions{
		{Target: "HEAD", Tags: []string{"valid", "../invalid"}}, {Target: "HEAD", Tags: []string{""}},
		{Target: "HEAD"}, {Tags: []string{"valid"}}, {Remote: "missing", Target: "HEAD", Tags: []string{"valid"}},
	} {
		report, err = PublishTags(context.Background(), repo, opts)
		requireProblem(t, err, "git.invalid_argument", false)
		if report.PushAttempted {
			t.Fatal(report)
		}
	}
	report, err = PublishTags(context.Background(), repo, PublishTagsOptions{Target: "missing-ref", Tags: []string{"valid"}})
	if err == nil || report.PushAttempted {
		t.Fatal(report, err)
	}
	runGit(t, repo, "remote", "set-url", "--add", "--push", "origin", remote)
	runGit(t, repo, "remote", "set-url", "--add", "--push", "origin", filepath.Join(t.TempDir(), "other"))
	_, err = PublishTags(context.Background(), repo, PublishTagsOptions{Target: first, Tags: []string{"valid"}})
	requireProblem(t, err, "git.ambiguous_remote", false)
}

// +case=A per-ref rejection leaves the whole batch absent; unsupported atomic push has no non-atomic fallback
func TestPublishTagsAtomicRejection(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "hook", true: "unsupported"}[unsupported], func(t *testing.T) {
			repo, remote, sha := tagFixture(t)
			if unsupported {
				runGit(t, remote, "config", "receive.advertiseAtomic", "false")
			} else {
				hook := filepath.Join(remote, "hooks", "update")
				runGit(t, remote, "config", "core.hooksPath", filepath.Dir(hook))
				if err := os.WriteFile(hook, []byte("#!/bin/sh\n[ \"$1\" != \"refs/tags/rejected\" ]\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			report, err := PublishTags(context.Background(), repo, PublishTagsOptions{Target: sha, Tags: []string{"allowed", "rejected"}})
			if unsupported {
				requireProblem(t, err, "git.tag_publish_unknown", true)
			} else {
				requireProblem(t, err, "git.tag_publish_failed", false)
			}
			if !report.PushAttempted || report.Confirmed || runGit(t, remote, "tag", "--list") != "" {
				t.Fatal(report)
			}
			if !unsupported {
				// The same remote accepts one ref without --atomic: this fixture detects a lost atomic flag.
				result := command.Run(context.Background(), repo, 5*time.Second, "push", remote, sha+":refs/tags/allowed", sha+":refs/tags/rejected")
				if result.Err == nil || runGit(t, remote, "tag", "--list") != "allowed" {
					t.Fatal(result)
				}
			}
		})
	}
}

// +case=A timed-out push is reconciled without retry; failed verification never reports stale preflight facts as current
func TestPublishTagsReconciliation(t *testing.T) {
	for _, mode := range []string{"applied_timeout", "unapplied_timeout", "verify_failure", "canceled_after_apply", "transport_failure", "remote_failure"} {
		t.Run(mode, func(t *testing.T) {
			repo, _, sha := tagFixture(t)
			pushes, reads := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := func(ctx context.Context, repo string, budget time.Duration, args ...string) command.Result {
				if args[0] == "ls-remote" {
					reads++
					if reads == 2 && mode == "verify_failure" {
						return command.Result{ExitCode: 1, Err: errors.New("offline")}
					}
				}
				if args[0] == "push" {
					pushes++
					if mode != "unapplied_timeout" && mode != "transport_failure" && mode != "remote_failure" {
						r := command.Run(ctx, repo, budget, args...)
						if r.Err != nil {
							t.Fatal(r)
						}
					}
					if mode == "canceled_after_apply" {
						cancel()
					}
					if mode == "transport_failure" {
						return command.Result{ExitCode: 128, Err: errors.New("connection reset")}
					}
					if mode == "remote_failure" {
						return command.Result{ExitCode: 1, Err: errors.New("remote failure"), Stdout: "!\t" + sha + ":refs/tags/v1\t[remote failure] remote did not report status\n"}
					}
					return command.Result{ExitCode: -1, Err: context.DeadlineExceeded, Uncertain: true}
				}
				return command.Run(ctx, repo, budget, args...)
			}
			report, err := publishTags(ctx, repo, PublishTagsOptions{Target: sha, Tags: []string{"v1"}}, runner)
			if mode == "applied_timeout" {
				if err != nil || !report.Confirmed || report.Tags[0].Status != "present" {
					t.Fatal(report, err)
				}
			} else {
				requireProblem(t, err, "git.tag_publish_unknown", true)
				if report.Confirmed {
					t.Fatal(report)
				}
				if (mode == "verify_failure" || mode == "canceled_after_apply") && report.Tags[0].Status != "unknown" {
					t.Fatal(report)
				}
			}
			if pushes != 1 || reads != 2 {
				t.Fatal(pushes, reads)
			}
		})
	}
}

// +case=Moving HEAD cannot change the pinned commit; a concurrently created conflicting remote tag is never overwritten
func TestPublishTagsConcurrentChanges(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "head", true: "remote"}[conflict], func(t *testing.T) {
			repo, remote, first := tagFixture(t)
			runGit(t, repo, "commit", "--allow-empty", "-qm", "second")
			second := runGit(t, repo, "rev-parse", "HEAD")
			runGit(t, repo, "push", remote, second+":refs/heads/main")
			runner := func(ctx context.Context, repo string, budget time.Duration, args ...string) command.Result {
				if args[0] == "push" {
					if conflict {
						runGit(t, remote, "tag", "v1", first)
					} else {
						runGit(t, repo, "commit", "--allow-empty", "-qm", "moved")
					}
				}
				return command.Run(ctx, repo, budget, args...)
			}
			report, err := publishTags(context.Background(), repo, PublishTagsOptions{Target: "HEAD", Tags: []string{"v1", "v2"}}, runner)
			if conflict {
				requireProblem(t, err, "git.tag_publish_failed", false)
				if runGit(t, remote, "rev-parse", "refs/tags/v1") != first || runGit(t, remote, "tag", "--list", "v2") != "" {
					t.Fatal(report)
				}
			} else if err != nil || report.TargetCommit != second || runGit(t, remote, "rev-parse", "refs/tags/v1") != second {
				t.Fatal(report, err)
			}
		})
	}
}

func TestPublishTagsRelativePushPath(t *testing.T) {
	repo, remote, sha := tagFixture(t)
	runGit(t, repo, "remote", "set-url", "origin", "../remote.git")
	report, err := PublishTags(context.Background(), repo, PublishTagsOptions{Target: sha, Tags: []string{"v1"}})
	if err != nil || !report.Confirmed || runGit(t, remote, "rev-parse", "refs/tags/v1") != sha {
		t.Fatal(report, err)
	}
}
