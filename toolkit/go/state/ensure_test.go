package state

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
)

func ensureFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	t.Setenv("REPOKIT_HOME", t.TempDir())
	return worktreeFixture(t)
}
func requireEnsure(t *testing.T, root, path string, options EnsureWorktreeOptions) EnsureWorktreeReport {
	t.Helper()
	r, e := EnsureWorktree(context.Background(), root, path, options)
	if e != nil || !r.Confirmed {
		t.Fatalf("%+v %v", r, e)
	}
	return r
}

// +case=Create and dirty-checkout reuse are idempotent and never reset, fetch, install or renew creation time.
func TestEnsureCreateAndReuse(t *testing.T) {
	root, git := ensureFixture(t)
	target := filepath.Join(root, "nested", "topic 中文\n ")
	opts := EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"}
	r := requireEnsure(t, root, target, opts)
	if r.Action != "created" || !r.WriteAttempted || r.HeadSHA != strings.TrimSpace(git("rev-parse", "HEAD")) || r.BaseCommit != r.HeadSHA {
		t.Fatal(r)
	}
	info, e := Checkout(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	record := filepath.Join(info.GitDir, "repokit-created.json")
	before, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(target, "unsaved"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	opts.Base, opts.Remote = "nonexistent", "unavailable"
	r = requireEnsure(t, root, target, opts)
	after, _ := os.ReadFile(record)
	if r.Action != "reused" || r.WriteAttempted || string(before) != string(after) {
		t.Fatal(r)
	}
	content, _ := os.ReadFile(filepath.Join(target, "unsaved"))
	if string(content) != "keep" {
		t.Fatal("dirty data changed")
	}
	for _, dir := range []string{"node_modules", ".venv"} {
		if _, e = os.Stat(filepath.Join(target, dir)); !os.IsNotExist(e) {
			t.Fatal("unexpected dependency preparation", e)
		}
	}
}

// +case=Retained branch resumes its exact commit without consulting or resetting to the requested creation base.
func TestEnsureResumesBranch(t *testing.T) {
	root, git := ensureFixture(t)
	git("branch", "topic")
	head := strings.TrimSpace(git("rev-parse", "topic"))
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "advance")
	// A same-named tag must neither detach HEAD nor alter branch identity.
	git("tag", "topic")
	r := requireEnsure(t, root, filepath.Join(root, "topic"), EnsureWorktreeOptions{Branch: "topic", Base: "missing", Remote: "missing"})
	if r.Action != "resumed" || r.HeadSHA != head || r.BaseCommit != "" {
		t.Fatal(r)
	}
}

// +case=New remote branches use freshly fetched commits; failed refresh never falls back to a stale tracking ref.
func TestEnsureRefreshAndOfflineFailure(t *testing.T) {
	remote, remoteGit := ensureFixture(t)
	remoteGit("branch", "-M", "main")
	root, git := ensureFixture(t)
	git("remote", "add", "origin", remote)
	git("fetch", "origin", "main:refs/remotes/origin/main")
	stale := strings.TrimSpace(git("rev-parse", "origin/main"))
	remoteGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "advance")
	latest := strings.TrimSpace(remoteGit("rev-parse", "HEAD"))
	r := requireEnsure(t, root, filepath.Join(root, "fresh"), EnsureWorktreeOptions{Branch: "fresh", Base: "main", Remote: "origin"})
	if r.HeadSHA != latest || r.HeadSHA == stale || r.BaseCommit != latest {
		t.Fatal(r)
	}
	git("remote", "set-url", "origin", filepath.Join(root, "offline"))
	target := filepath.Join(root, "offline-target")
	r, e := EnsureWorktree(context.Background(), root, target, EnsureWorktreeOptions{Branch: "offline", Base: "main", Remote: "origin"})
	if e == nil || r.WriteAttempted || r.Confirmed {
		t.Fatal(r, e)
	}
	if _, e = os.Stat(target); !os.IsNotExist(e) {
		t.Fatal("created checkout after failed fetch", e)
	}
	cmd := exec.Command("git", "-C", root, "show-ref", "--verify", "--quiet", "refs/heads/offline")
	if e = cmd.Run(); e == nil {
		t.Fatal("created branch after failed fetch")
	}
}
func TestEnsureConflicts(t *testing.T) {
	for _, kind := range []string{"occupied", "foreign", "branch", "elsewhere", "missing", "symlink", "detached"} {
		t.Run(kind, func(t *testing.T) {
			root, git := ensureFixture(t)
			target := filepath.Join(root, "target")
			switch kind {
			case "occupied":
				if e := os.Mkdir(target, 0755); e != nil {
					t.Fatal(e)
				}
			case "foreign":
				foreign, _ := ensureFixture(t)
				target = foreign
			case "branch":
				git("worktree", "add", "-b", "different", target, "HEAD")
			case "elsewhere":
				git("worktree", "add", "-b", "topic", filepath.Join(root, "other"), "HEAD")
			case "missing":
				git("worktree", "add", "-b", "topic", target, "HEAD")
				if e := os.RemoveAll(target); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink(root, target); e != nil {
					t.Fatal(e)
				}
			case "detached":
				git("worktree", "add", "--detach", target, "HEAD")
			}
			r, e := EnsureWorktree(context.Background(), root, target, EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"})
			if e == nil || r.Confirmed || r.WriteAttempted {
				t.Fatal(kind, r, e)
			}
		})
	}
}

// +case=A replaced registered directory belonging to another clone is never accepted as the original checkout.
func TestEnsureRejectsReplacedRegisteredCheckout(t *testing.T) {
	root, git := ensureFixture(t)
	target := filepath.Join(root, "target")
	git("worktree", "add", "-b", "topic", target, "HEAD")
	if e := os.RemoveAll(target); e != nil {
		t.Fatal(e)
	}
	foreign, _ := ensureFixture(t)
	out, e := exec.Command("git", "clone", "-q", foreign, target).CombinedOutput()
	if e != nil {
		t.Fatal(string(out), e)
	}
	r, e := EnsureWorktree(context.Background(), root, target, EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"})
	if e == nil || r.Confirmed || r.WriteAttempted {
		t.Fatal(r, e)
	}
}

// +case=Concurrent callers share the checkout lease; one creates and one reuses. Cancellation while waiting creates nothing.
func TestEnsureLeaseAndConcurrency(t *testing.T) {
	root, _ := ensureFixture(t)
	info, e := Checkout(context.Background(), root)
	if e != nil {
		t.Fatal(e)
	}
	unlock, e := repositoryLease(context.Background(), filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	target := filepath.Join(root, "target")
	opts := EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"}
	r, e := EnsureWorktree(ctx, root, target, opts)
	cancel()
	unlock()
	if e == nil || r.WriteAttempted || r.Confirmed {
		t.Fatal(r, e)
	}
	type result struct {
		r EnsureWorktreeReport
		e error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() { r, e := EnsureWorktree(context.Background(), root, target, opts); results <- result{r, e} }()
	}
	actions := map[string]int{}
	for range 2 {
		result := <-results
		if result.e != nil || !result.r.Confirmed {
			t.Fatal(result)
		}
		actions[result.r.Action]++
	}
	if actions["created"] != 1 || actions["reused"] != 1 {
		t.Fatal(actions)
	}
}

// +case=Publication failure retains a confirmed usable checkout with a structured warning, never a creation retry signal.
func TestEnsureObservationWarning(t *testing.T) {
	root, _ := ensureFixture(t)
	home := filepath.Join(t.TempDir(), "file")
	if e := os.WriteFile(home, []byte("occupied"), 0600); e != nil {
		t.Fatal(e)
	}
	r := requireEnsure(t, root, filepath.Join(root, "target"), EnsureWorktreeOptions{Branch: "topic", Base: "HEAD", Home: home})
	if len(r.Diagnostics) == 0 || r.Diagnostics[0].Code != "state.observation_unavailable" {
		t.Fatal(r)
	}
}
func TestEnsureInvalidRequest(t *testing.T) {
	root, _ := ensureFixture(t)
	for _, opts := range []EnsureWorktreeOptions{{Branch: "topic"}, {Base: "HEAD"}, {Branch: "../bad", Base: "HEAD"}, {Branch: "@{-1}", Base: "HEAD"}} {
		r, e := EnsureWorktree(context.Background(), root, "target", opts)
		var structured *errs.Error
		if e == nil || r.WriteAttempted || !errors.As(e, &structured) {
			t.Fatal(r, e)
		}
	}
}

// +case=Cancellation after Git creates a checkout reports unknown; it never rolls back the checkout or invites an unverified retry.
func TestEnsureCancelledWrite(t *testing.T) {
	root, _ := ensureFixture(t)
	realGit, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(t.TempDir(), "created")
	scripts := t.TempDir()
	script := `#!/bin/sh
if [ "$3" = worktree ] && [ "$4" = add ]; then
  "$ENSURE_REAL_GIT" "$@" || exit $?
  : > "$ENSURE_CREATED_MARKER"
  sleep 30
else
  exec "$ENSURE_REAL_GIT" "$@"
fi
`
	if e = os.WriteFile(filepath.Join(scripts, "git"), []byte(script), 0755); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ENSURE_REAL_GIT", realGit)
	t.Setenv("ENSURE_CREATED_MARKER", marker)
	t.Setenv("PATH", scripts+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
				if _, e := os.Stat(marker); e == nil {
					cancel()
					return
				}
			}
		}
	}()
	target := filepath.Join(root, "target")
	r, e := EnsureWorktree(ctx, root, target, EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"})
	var issue *errs.Error
	if e == nil || !errors.As(e, &issue) || !issue.Uncertain || !r.WriteAttempted || r.Confirmed {
		t.Fatal(r, e)
	}
	if _, e = os.Stat(filepath.Join(target, ".git")); e != nil {
		t.Fatal("created checkout was removed", e)
	}
}
