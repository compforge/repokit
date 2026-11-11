package rebase

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

func command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, out)
	}
	return strings.TrimSpace(string(out))
}
func write(t *testing.T, path, body string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T, conflict bool) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	command(t, root, "init", "-q", "-b", "main", repo)
	command(t, root, "init", "--bare", "-q", remote)
	command(t, repo, "config", "user.name", "Test")
	command(t, repo, "config", "user.email", "test@example.com")
	write(t, filepath.Join(repo, "shared"), "base\n")
	command(t, repo, "add", ".")
	command(t, repo, "commit", "-qm", "base")
	command(t, repo, "remote", "add", "origin", remote)
	command(t, repo, "push", "-q", "origin", "main")
	command(t, repo, "checkout", "-qb", "feature/topic")
	file := "feature"
	if conflict {
		file = "shared"
	}
	write(t, filepath.Join(repo, file), "feature\n")
	command(t, repo, "add", ".")
	command(t, repo, "commit", "-qm", "feature")
	command(t, repo, "push", "-q", "origin", "feature/topic")
	command(t, repo, "checkout", "-q", "main")
	file = "target"
	if conflict {
		file = "shared"
	}
	write(t, filepath.Join(repo, file), "target\n")
	command(t, repo, "add", ".")
	command(t, repo, "commit", "-qm", "target")
	command(t, repo, "push", "-q", "origin", "main")
	command(t, repo, "checkout", "-q", "feature/topic")
	return repo, remote
}
func start(t *testing.T, repo string) Report {
	t.Helper()
	r, e := Run(context.Background(), repo, Start, Options{Branch: "feature/topic", Target: "main"})
	if e != nil || !r.Confirmed {
		for cause := e; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("cause: %v", cause)
		}
		t.Fatal(r, e)
	}
	return r
}
func expect(t *testing.T, repo string, action Action, phase string) Report {
	t.Helper()
	r, e := Run(context.Background(), repo, action, Options{})
	if e != nil || !r.Confirmed || r.Phase != phase {
		t.Fatal(r, e)
	}
	return r
}
func problem(t *testing.T, e error, code string, uncertain bool) {
	t.Helper()
	var p *errs.Error
	if !errors.As(e, &p) || p.Code != "git.rebase_"+code || p.Uncertain != uncertain {
		t.Fatalf("want %s uncertain=%t, got %+v", code, uncertain, e)
	}
}

// +case=Freeze the creation baseline and source lease; publish exactly once even after the target moves, preserving tags and unrelated branches.
func TestLifecycle(t *testing.T) {
	repo, remote := fixture(t, false)
	command(t, repo, "tag", "feature/topic")
	command(t, repo, "config", "push.followTags", "true")
	r := start(t, repo)
	if r.Phase != "ready" || r.Transaction.ExpectedRemote == r.Head {
		t.Fatal(r)
	}
	command(t, remote, "update-ref", "refs/heads/main", r.Transaction.ExpectedRemote)
	r = expect(t, repo, Finish, "published")
	if !r.PushAttempted || command(t, remote, "rev-parse", "refs/heads/feature/topic") != r.Head {
		t.Fatal(r)
	}
	if command(t, remote, "tag", "--list") != "" {
		t.Fatal("published unwanted tags")
	}
	r = expect(t, repo, Finish, "published")
	if r.PushAttempted {
		t.Fatal("replayed push")
	}
	_, e := Run(context.Background(), repo, Abort, Options{})
	problem(t, e, "not_paused", false)
}
func TestConflictContinueAndAbort(t *testing.T) {
	for _, action := range []Action{Continue, Abort} {
		t.Run(string(action), func(t *testing.T) {
			repo, remote := fixture(t, true)
			r := start(t, repo)
			if r.Phase != "paused" || len(r.Conflicts) != 1 {
				t.Fatal(r)
			}
			if action == Abort {
				r = expect(t, repo, Abort, "aborted")
				if r.Head != r.Transaction.StartedHead {
					t.Fatal(r)
				}
				expect(t, repo, Abort, "aborted")
				return
			}
			expect(t, repo, Continue, "paused")
			write(t, filepath.Join(repo, "shared"), "resolved\n")
			command(t, repo, "add", "shared")
			expect(t, repo, Continue, "ready")
			expect(t, repo, Continue, "ready")
			r = expect(t, repo, Finish, "published")
			if command(t, remote, "rev-parse", "refs/heads/feature/topic") != r.Head {
				t.Fatal(r)
			}
		})
	}
}
func TestRemoteMoveAndDestinationChange(t *testing.T) {
	repo, remote := fixture(t, false)
	r := start(t, repo)
	command(t, remote, "update-ref", "refs/heads/feature/topic", r.Transaction.TargetCommit)
	_, e := Run(context.Background(), repo, Finish, Options{})
	problem(t, e, "lease_conflict", false)
	command(t, remote, "update-ref", "refs/heads/feature/topic", r.Transaction.ExpectedRemote)
	command(t, repo, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing"))
	_, e = Run(context.Background(), repo, Finish, Options{})
	problem(t, e, "destination_changed", false)
}

// +case=An acknowledged or unacknowledged successful push is reconciled at the frozen destination before any retry.
func TestLostPushResponse(t *testing.T) {
	repo, _ := fixture(t, false)
	start(t, repo)
	pushed := false
	run := func(ctx context.Context, repo string, d time.Duration, args ...string) gitworktree.GitResult {
		if pushed && args[0] == "ls-remote" {
			return gitworktree.GitResult{Err: errors.New("observation unavailable"), ExitCode: -1}
		}
		r := gitworktree.Run(ctx, repo, d, args...)
		if args[0] == "push" {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			pushed = true
			return gitworktree.GitResult{Err: context.DeadlineExceeded, Uncertain: true, ExitCode: -1}
		}
		return r
	}
	r, e := execute(context.Background(), repo, Finish, Options{}, run)
	problem(t, e, "unknown", true)
	if !r.PushAttempted || r.Confirmed || r.Transaction.PublishHead == "" {
		t.Fatal(r)
	}
	r = expect(t, repo, Finish, "published")
	if r.PushAttempted {
		t.Fatal("must reconcile without another push")
	}
}
func TestRewriteUncertaintyAndForeignOperation(t *testing.T) {
	repo, _ := fixture(t, false)
	run := func(ctx context.Context, repo string, d time.Duration, args ...string) gitworktree.GitResult {
		r := gitworktree.Run(ctx, repo, d, args...)
		if len(args) > 4 && args[4] == "rebase" {
			r.Err = context.Canceled
			r.Uncertain = true
		}
		return r
	}
	r, e := execute(context.Background(), repo, Start, Options{Branch: "feature/topic", Target: "main"}, run)
	problem(t, e, "unknown", true)
	if r.Confirmed {
		t.Fatal(r)
	}
	expect(t, repo, Status, "ready")
	// Another Git rebase with a different identity must not be continued using this record.
	native := filepath.Join(repo, ".git", "rebase-merge")
	if e = os.Mkdir(native, 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(native, "head-name"), "refs/heads/other\n")
	write(t, filepath.Join(native, "orig-head"), r.Transaction.StartedHead+"\n")
	write(t, filepath.Join(native, "onto"), r.Transaction.TargetCommit+"\n")
	_, e = Run(context.Background(), repo, Continue, Options{})
	problem(t, e, "identity_mismatch", false)
}
func TestLinkedCheckoutAndLockCancellation(t *testing.T) {
	repo, _ := fixture(t, true)
	command(t, repo, "checkout", "-q", "main")
	linked := filepath.Join(t.TempDir(), "linked 工作区\n")
	command(t, repo, "worktree", "add", "-q", linked, "feature/topic")
	start(t, linked)
	expect(t, repo, Status, "none")
	expect(t, linked, Abort, "aborted")
	info, e := gitworktree.Local(context.Background(), linked)
	if e != nil {
		t.Fatal(e)
	}
	unlock, e := gitworktree.AcquireLease(context.Background(), filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := Run(ctx, linked, Start, Options{Branch: "feature/topic", Target: "main"})
	if e == nil || r.WriteAttempted {
		t.Fatal(r, e)
	}
}
func TestPreflightPreservesDirtyCheckoutAndCorruptRecord(t *testing.T) {
	repo, _ := fixture(t, false)
	head := command(t, repo, "rev-parse", "HEAD")
	write(t, filepath.Join(repo, "untracked"), "valuable")
	r, e := Run(context.Background(), repo, Start, Options{Branch: "feature/topic", Target: "main"})
	problem(t, e, "dirty", false)
	if r.WriteAttempted || command(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal(r)
	}
	write(t, filepath.Join(repo, ".git", recordName), "{broken")
	_, e = Run(context.Background(), repo, Start, Options{Branch: "feature/topic", Target: "main"})
	problem(t, e, "record_invalid", false)
}
func TestSplitFetchPushDestinationAndPublicationWarning(t *testing.T) {
	repo, remote := fixture(t, false)
	fetch := filepath.Join(t.TempDir(), "fetch.git")
	command(t, repo, "clone", "--bare", "-q", remote, fetch)
	command(t, repo, "remote", "set-url", "origin", fetch)
	command(t, repo, "remote", "set-url", "--push", "origin", remote)
	start(t, repo)
	run := func(ctx context.Context, repo string, d time.Duration, args ...string) gitworktree.GitResult {
		r := gitworktree.Run(ctx, repo, d, args...)
		if args[0] == "push" && r.Err == nil {
			path := filepath.Join(repo, ".git", recordName)
			if e := os.Rename(path, path+".backup"); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(path, 0700); e != nil {
				t.Fatal(e)
			}
		}
		return r
	}
	r, e := execute(context.Background(), repo, Finish, Options{}, run)
	if e != nil || !r.Confirmed || r.Phase != "published" || len(r.Diagnostics) != 1 {
		t.Fatal(r, e)
	}
	if command(t, fetch, "rev-parse", "refs/heads/feature/topic") == r.Head {
		t.Fatal("published to fetch destination")
	}
}

func TestMovingSourceBeforeRewriteAndMissingTarget(t *testing.T) {
	repo, remote := fixture(t, false)
	original := command(t, repo, "rev-parse", "HEAD")
	queries := 0
	run := func(ctx context.Context, repo string, d time.Duration, args ...string) gitworktree.GitResult {
		if args[0] == "ls-remote" {
			queries++
			if queries == 2 {
				command(t, remote, "update-ref", "refs/heads/feature/topic", command(t, repo, "rev-parse", "refs/heads/main"))
			}
		}
		return gitworktree.Run(ctx, repo, d, args...)
	}
	r, e := execute(context.Background(), repo, Start, Options{Branch: "feature/topic", Target: "main"}, run)
	problem(t, e, "lease_conflict", false)
	if r.WriteAttempted || command(t, repo, "rev-parse", "HEAD") != original {
		t.Fatal(r)
	}
	command(t, remote, "update-ref", "refs/heads/feature/topic", original)
	r, e = Run(context.Background(), repo, Start, Options{Branch: "feature/topic", Target: "missing"})
	if e == nil || r.WriteAttempted {
		t.Fatal(r, e)
	}
	if _, e = os.Stat(filepath.Join(repo, ".git", recordName)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("must not save transaction before preflight completes", e)
	}
}
func TestFreezePublicationAndCancelRecovery(t *testing.T) {
	repo, _ := fixture(t, false)
	start(t, repo)
	run := func(ctx context.Context, repo string, d time.Duration, args ...string) gitworktree.GitResult {
		if args[0] == "push" {
			return gitworktree.GitResult{Err: context.Canceled, Uncertain: true, ExitCode: -1}
		}
		return gitworktree.Run(ctx, repo, d, args...)
	}
	r, e := execute(context.Background(), repo, Finish, Options{}, run)
	problem(t, e, "unknown", true)
	frozen := r.Transaction.PublishHead
	expect(t, repo, Status, "publishing")
	command(t, repo, "commit", "--allow-empty", "-qm", "new work after publish intent")
	r, e = Run(context.Background(), repo, Finish, Options{})
	problem(t, e, "identity_mismatch", false)
	if r.PushAttempted || r.Transaction.PublishHead != frozen {
		t.Fatal(r)
	}
}
func TestAbortInterruptedAfterEffect(t *testing.T) {
	repo, _ := fixture(t, true)
	start(t, repo)
	run := func(ctx context.Context, repo string, d time.Duration, args ...string) gitworktree.GitResult {
		r := gitworktree.Run(ctx, repo, d, args...)
		if len(args) == 2 && args[1] == "--abort" {
			r.Err = context.Canceled
			r.Uncertain = true
		}
		return r
	}
	_, e := execute(context.Background(), repo, Abort, Options{}, run)
	problem(t, e, "unknown", true)
	expect(t, repo, Status, "unknown")
	expect(t, repo, Abort, "aborted")
}
