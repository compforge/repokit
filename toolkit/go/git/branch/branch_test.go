package branch

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

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func write(t *testing.T, dir, file, body string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(dir, file), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")
	write(t, dir, "file", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	return dir
}
func changes(t *testing.T, dir string) {
	write(t, dir, "file", "staged\n")
	git(t, dir, "add", "file")
	write(t, dir, "file", "unstaged\n")
	write(t, dir, "new file", "untracked\n")
}
func preserved(t *testing.T, dir string) {
	t.Helper()
	if got := git(t, dir, "show", ":file"); got != "staged" {
		t.Fatal(got)
	}
	b, e := os.ReadFile(filepath.Join(dir, "file"))
	if e != nil || string(b) != "unstaged\n" {
		t.Fatal(string(b), e)
	}
	b, e = os.ReadFile(filepath.Join(dir, "new file"))
	if e != nil || string(b) != "untracked\n" {
		t.Fatal(string(b), e)
	}
}
func checkError(t *testing.T, e error, code string) {
	t.Helper()
	var p *errs.Error
	if !errors.As(e, &p) || p.Code != "git.branch_"+code {
		t.Fatalf("want %s, got %+v", code, e)
	}
}

// +case=Explicit carry preserves partial staging and untracked files and retains an exact backup independently of the stash stack.
func TestCarryAndReuse(t *testing.T) {
	dir := fixture(t)
	changes(t, dir)
	r, e := Create(context.Background(), dir, Options{Name: "topic", Base: "main", CarryChanges: true})
	if e != nil || !r.Confirmed || !r.BranchConfirmed || !r.ChangesRestored || r.StashOID == "" || r.Action != "created" {
		t.Fatal(r, e)
	}
	preserved(t, dir)
	if git(t, dir, "rev-parse", "stash@{0}") != r.StashOID {
		t.Fatal(r)
	}
	if stat, e := os.Stat(r.RecoveryPath); e != nil || stat.Mode().Perm() != 0600 {
		t.Fatal(stat, e)
	}
	again, e := Create(context.Background(), dir, Options{Name: "topic", Base: "missing"})
	if e != nil || again.Action != "reused" || again.WriteAttempted {
		t.Fatal(again, e)
	}
	preserved(t, dir)
	status, e := Status(context.Background(), dir, "topic")
	if e != nil || !status.Confirmed || status.WriteAttempted || status.StashOID != r.StashOID {
		t.Fatal(status, e)
	}
}
func TestPreconditions(t *testing.T) {
	for _, test := range []struct {
		name, code string
		setup      func(*testing.T, string)
		opts       Options
	}{
		{"dirty", "dirty", changes, Options{Name: "topic", Base: "main"}},
		{"existing", "exists", func(t *testing.T, d string) { git(t, d, "branch", "topic") }, Options{Name: "topic", Base: "main"}},
		{"busy", "busy", func(t *testing.T, d string) { write(t, d, ".git/MERGE_HEAD", git(t, d, "rev-parse", "HEAD")) }, Options{Name: "topic", Base: "main"}},
		{"missing_base", "base_unavailable", changes, Options{Name: "topic", Base: "missing", CarryChanges: true}},
		{"invalid_name", "invalid_argument", func(*testing.T, string) {}, Options{Name: "-b", Base: "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := fixture(t)
			test.setup(t, d)
			before := git(t, d, "status", "--porcelain=v1")
			r, e := Create(context.Background(), d, test.opts)
			checkError(t, e, test.code)
			if r.WriteAttempted || git(t, d, "branch", "--show-current") != "main" || git(t, d, "status", "--porcelain=v1") != before {
				t.Fatal(r)
			}
		})
	}
}
func TestRemoteBaseRefreshed(t *testing.T) {
	d := fixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, d, "init", "--bare", remote)
	git(t, d, "remote", "add", "origin", remote)
	git(t, d, "push", "origin", "main")
	git(t, d, "commit", "--allow-empty", "-qm", "new base")
	head := git(t, d, "rev-parse", "HEAD")
	git(t, d, "push", "origin", "main")
	git(t, d, "update-ref", "refs/remotes/origin/main", "HEAD~1")
	r, e := Create(context.Background(), d, Options{Name: "topic", Base: "main", Remote: "origin"})
	if e != nil || !r.Confirmed || r.BaseCommit != head || git(t, d, "rev-parse", "HEAD") != head {
		t.Fatal(r, e)
	}
}

// +case=Conflicted carry keeps the created branch and backup; retries never apply a stash twice.
func TestRestoreConflictAndNoReplay(t *testing.T) {
	d := fixture(t)
	base := git(t, d, "rev-parse", "HEAD")
	write(t, d, "file", "target\n")
	git(t, d, "commit", "-qam", "target")
	target := git(t, d, "rev-parse", "HEAD")
	git(t, d, "checkout", "--detach", base)
	write(t, d, "file", "local\n")
	r, e := Create(context.Background(), d, Options{Name: "topic", Base: target, CarryChanges: true})
	checkError(t, e, "restore_failed")
	if !r.BranchConfirmed || r.ChangesRestored || r.Confirmed || r.StashOID == "" || r.Phase != "conflict" {
		t.Fatal(r)
	}
	before := git(t, d, "status", "--porcelain=v1")
	again, e := Create(context.Background(), d, Options{Name: "topic", Base: target, CarryChanges: true})
	checkError(t, e, "recovery_required")
	if again.WriteAttempted || git(t, d, "status", "--porcelain=v1") != before {
		t.Fatal(again)
	}
}

// +case=A known failed checkout restores the original index; an uncertain write retains evidence without replay.
func TestCheckoutFailureAndUnknownWrites(t *testing.T) {
	for _, point := range []string{"checkout_failed", "checkout_unknown", "stash_unknown", "apply_unknown"} {
		t.Run(point, func(t *testing.T) {
			d := fixture(t)
			changes(t, d)
			injected := func(ctx context.Context, repo string, timeout time.Duration, args ...string) gitworktree.GitResult {
				match := args[0] == "checkout" && strings.HasPrefix(point, "checkout") || args[0] == "stash" && args[1] == "push" && point == "stash_unknown" || args[0] == "stash" && args[1] == "apply" && point == "apply_unknown"
				if !match {
					return gitworktree.Run(ctx, repo, timeout, args...)
				}
				if point == "checkout_failed" {
					return gitworktree.GitResult{ExitCode: 1, Err: errors.New("rejected")}
				}
				result := gitworktree.Run(ctx, repo, timeout, args...)
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				return gitworktree.GitResult{ExitCode: -1, Err: context.DeadlineExceeded, Uncertain: true}
			}
			r, e := execute(context.Background(), d, Options{Name: "topic", Base: "main", CarryChanges: true}, false, injected)
			if point == "checkout_failed" {
				checkError(t, e, "checkout_failed")
				preserved(t, d)
				if r.BranchConfirmed || !r.ChangesRestored || r.Phase != "failed" {
					t.Fatal(r)
				}
				return
			}
			checkError(t, e, "unknown")
			if r.Confirmed || r.StashOID == "" {
				t.Fatal(r)
			}
			status, e := Status(context.Background(), d, "topic")
			if e != nil || status.Confirmed || status.StashOID != r.StashOID {
				t.Fatal(status, e)
			}
			_, e = Create(context.Background(), d, Options{Name: "topic", Base: "main", CarryChanges: true})
			checkError(t, e, "recovery_required")
			if point == "apply_unknown" {
				preserved(t, d)
			}
		})
	}
}
func TestIgnoredCollisionIsPreserved(t *testing.T) {
	d := fixture(t)
	base := git(t, d, "rev-parse", "HEAD")
	write(t, d, "ignored", "target\n")
	git(t, d, "add", "ignored")
	git(t, d, "commit", "-qm", "target")
	target := git(t, d, "rev-parse", "HEAD")
	git(t, d, "checkout", "--detach", base)
	write(t, d, ".git/info/exclude", "ignored\n")
	write(t, d, "ignored", "valuable\n")
	r, e := Create(context.Background(), d, Options{Name: "topic", Base: target})
	checkError(t, e, "checkout_failed")
	b, _ := os.ReadFile(filepath.Join(d, "ignored"))
	if string(b) != "valuable\n" || r.BranchConfirmed {
		t.Fatal(string(b), r)
	}
}

func TestUnknownStatusPreventsWrites(t *testing.T) {
	d := fixture(t)
	changes(t, d)
	run := func(ctx context.Context, repo string, timeout time.Duration, args ...string) gitworktree.GitResult {
		if args[0] == "status" {
			return gitworktree.GitResult{ExitCode: 1, Err: errors.New("unavailable")}
		}
		return gitworktree.Run(ctx, repo, timeout, args...)
	}
	r, e := execute(context.Background(), d, Options{Name: "topic", Base: "main", CarryChanges: true}, false, run)
	checkError(t, e, "query_failed")
	if r.WriteAttempted || git(t, d, "stash", "list") != "" {
		t.Fatal(r)
	}
	preserved(t, d)
}

// +case=Failure to publish the post-apply record preserves confirmed Git effects and a warning, never an invitation to replay.
func TestConfirmedRestoreSurvivesRecordFailure(t *testing.T) {
	d := fixture(t)
	changes(t, d)
	run := func(ctx context.Context, repo string, timeout time.Duration, args ...string) gitworktree.GitResult {
		r := gitworktree.Run(ctx, repo, timeout, args...)
		if args[0] == "stash" && args[1] == "apply" && r.Err == nil {
			path := recordPath(filepath.Join(d, ".git"), "topic")
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(path, 0700); e != nil {
				t.Fatal(e)
			}
		}
		return r
	}
	r, e := execute(context.Background(), d, Options{Name: "topic", Base: "main", CarryChanges: true}, false, run)
	if e != nil || !r.Confirmed || !r.ChangesRestored || len(r.Diagnostics) != 1 {
		t.Fatal(r, e)
	}
	preserved(t, d)
}

func TestSharedLeaseHonorsCallerCancellation(t *testing.T) {
	d := fixture(t)
	unlock, e := gitworktree.AcquireLease(context.Background(), filepath.Join(d, ".git", "repokit-worktrees.lock"))
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, e := Create(ctx, d, Options{Name: "topic", Base: "main"})
	if !errors.Is(e, context.DeadlineExceeded) || r.WriteAttempted || git(t, d, "branch", "--show-current") != "main" {
		t.Fatal(r, e)
	}
}

func TestLinkedCheckoutKeepsPrivateRecoveryRecord(t *testing.T) {
	d := fixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, d, "worktree", "add", "-b", "old", linked)
	changes(t, linked)
	r, e := Create(context.Background(), linked, Options{Name: "topic", Base: "main", CarryChanges: true})
	if e != nil || !r.Confirmed {
		t.Fatal(r, e)
	}
	info, e := gitworktree.Local(context.Background(), linked)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(r.RecoveryPath, info.GitDir+string(filepath.Separator)) || info.GitDir == info.CommonDir {
		t.Fatal(r, info)
	}
	preserved(t, linked)
	primary, e := Status(context.Background(), d, "topic")
	if e != nil || primary.Phase != "none" || primary.StashOID != "" {
		t.Fatal(primary, e)
	}
}
