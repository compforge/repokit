package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo}, args...)...)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSuffix(string(b), "\n")
}
func put(t *testing.T, repo, path, body string) {
	t.Helper()
	p := filepath.Join(repo, path)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "topic")
	git(t, repo, "config", "user.name", "Fixture")
	git(t, repo, "config", "user.email", "fixture@example.invalid")
	git(t, repo, "config", "core.hooksPath", filepath.Join(repo, "hooks"))
	put(t, repo, "a", "base\n")
	put(t, repo, "b", "base\n")
	git(t, repo, "add", "a", "b")
	git(t, repo, "commit", "-qm", "base")
	return repo
}
func plan(t *testing.T, repo string, s Selection) Report {
	t.Helper()
	r, e := Plan(context.Background(), repo, s)
	if e != nil {
		t.Fatal(r, e)
	}
	return r
}
func create(t *testing.T, repo string, p Report) Report {
	t.Helper()
	r, e := Create(context.Background(), repo, Options{Intent: p.Intent, Message: "change"})
	if e != nil || !r.Confirmed || !r.IndexInstalled {
		t.Fatal(r, e)
	}
	return r
}
func bytes(t *testing.T, repo string) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(repo, ".git/index"))
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func hook(t *testing.T, repo, name, body string) {
	t.Helper()
	put(t, repo, "hooks/"+name, "#!/bin/sh\n"+body+"\n")
	if e := os.Chmod(filepath.Join(repo, "hooks", name), 0700); e != nil {
		t.Fatal(e)
	}
}

// +case=Staged mode preserves partial hunks; plan never installs its index and check never writes.
func TestStagedContentAndCheck(t *testing.T) {
	repo := fixture(t)
	put(t, repo, "a", "staged\n")
	git(t, repo, "add", "a")
	put(t, repo, "a", "working\n")
	before := bytes(t, repo)
	p := plan(t, repo, Selection{Mode: "staged"})
	if bytes(t, repo) != before {
		t.Fatal("plan mutated index")
	}
	r := create(t, repo, p)
	if git(t, repo, "show", "HEAD:a") != "staged" {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(repo, "a"))
	if string(b) != "working\n" {
		t.Fatal("worktree changed")
	}
	before = bytes(t, repo)
	r, e := Check(context.Background(), repo, p.Intent)
	if e != nil || !r.Confirmed || r.Attempted || bytes(t, repo) != before {
		t.Fatal(r, e)
	}
	if _, e = Create(context.Background(), repo, Options{Intent: p.Intent, Message: "again"}); e == nil {
		t.Fatal("replayed stale intent")
	}
}
func TestLiteralSelectionAndScope(t *testing.T) {
	repo := fixture(t)
	name := "src/ [x]\n中文 "
	put(t, repo, name, "literal")
	put(t, repo, "a", "leave")
	if e := os.Symlink("../a", filepath.Join(repo, "src/link")); e != nil {
		t.Fatal(e)
	}
	p := plan(t, repo, Selection{Mode: "files", Paths: []string{"src"}})
	if len(p.Changes) != 2 {
		t.Fatal(p)
	}
	create(t, repo, p)
	if git(t, repo, "show", "HEAD:a") != "base" || git(t, repo, "show", "HEAD:"+name) != "literal" {
		t.Fatal("wrong content")
	}
	put(t, repo, "b", "staged")
	git(t, repo, "add", "b")
	put(t, repo, "b", "partial")
	before := bytes(t, repo)
	if _, e := Plan(context.Background(), repo, Selection{Mode: "files", Paths: []string{"a"}}); e == nil || bytes(t, repo) != before {
		t.Fatal("outside staging not preserved", e)
	}
	for _, path := range []string{"", "../outside", "/tmp/outside", "missing"} {
		if _, e := Plan(context.Background(), repo, Selection{Mode: "files", Paths: []string{path}}); e == nil || bytes(t, repo) != before {
			t.Fatal(path, e)
		}
	}
}
func TestTrackedDeletionRenameAndUntracked(t *testing.T) {
	repo := fixture(t)
	git(t, repo, "rm", "--cached", "a")
	git(t, repo, "mv", "b", "renamed")
	put(t, repo, "untracked", "stay")
	p := plan(t, repo, Selection{Mode: "tracked"})
	if len(p.Changes) != 3 {
		t.Fatal(p)
	}
	create(t, repo, p)
	if got := git(t, repo, "ls-tree", "--name-only", "HEAD"); got != "renamed" {
		t.Fatal(got)
	}
	if _, e := os.Stat(filepath.Join(repo, "a")); e != nil {
		t.Fatal(e)
	}
}
func TestPlanRejectsConcurrentChanges(t *testing.T) {
	for _, mutation := range []string{"index", "content", "head", "branch"} {
		t.Run(mutation, func(t *testing.T) {
			repo := fixture(t)
			put(t, repo, "a", "selected")
			p := plan(t, repo, Selection{Mode: "files", Paths: []string{"a"}})
			switch mutation {
			case "index":
				put(t, repo, "b", "other")
				git(t, repo, "add", "b")
			case "content":
				put(t, repo, "a", "later")
			case "head":
				git(t, repo, "commit", "--allow-empty", "-qm", "other")
			case "branch":
				git(t, repo, "checkout", "-qb", "other")
			}
			before := bytes(t, repo)
			head := git(t, repo, "rev-parse", "HEAD")
			r, e := Create(context.Background(), repo, Options{Intent: p.Intent, Message: "change"})
			if e == nil || r.Attempted || bytes(t, repo) != before || git(t, repo, "rev-parse", "HEAD") != head {
				t.Fatal(r, e)
			}
		})
	}
}

// +case=Native hook failure retains the original index; hook tree changes are reported as actual unconfirmed commits and never rolled back.
func TestHooksAndRecovery(t *testing.T) {
	for _, kind := range []string{"reject", "tree", "message", "post_response_lost"} {
		t.Run(kind, func(t *testing.T) {
			repo := fixture(t)
			put(t, repo, "a", "selected")
			p := plan(t, repo, Selection{Mode: "files", Paths: []string{"a"}})
			before := bytes(t, repo)
			switch kind {
			case "reject":
				hook(t, repo, "pre-commit", "exit 1")
			case "tree":
				hook(t, repo, "pre-commit", "echo hook > b\ngit add b")
			case "message":
				hook(t, repo, "commit-msg", "echo modified > \"$1\"")
			case "post_response_lost":
				// The ref is already updated when this hook runs. Interrupt the Git
				// response at that event rather than racing preparation against a short deadline.
				hook(t, repo, "post-commit", `kill -TERM "$PPID"`)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r, e := Create(ctx, repo, Options{Intent: p.Intent, Message: "change"})
			if kind == "post_response_lost" && (len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != "git.commit_reconciled") {
				t.Fatal("lost Git response was not reconciled", r, e)
			}
			if kind == "message" || kind == "post_response_lost" {
				if e != nil || !r.Confirmed || !r.IndexInstalled {
					t.Fatal(r, e)
				}
				return
			}
			if e == nil || r.Confirmed || r.RecoveryPath == "" || bytes(t, repo) != before {
				t.Fatal(r, e)
			}
			if _, e := os.Stat(filepath.Join(r.RecoveryPath, "intent.json")); e != nil {
				t.Fatal(e)
			}
			if kind == "tree" && (r.Commit == "" || r.ActualTree == p.Tree) {
				t.Fatal(r)
			}
		})
	}
}
func TestGitlinkRegistrationUsesCandidate(t *testing.T) {
	repo := fixture(t)
	head := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",nested")
	before := bytes(t, repo)
	if _, e := Plan(context.Background(), repo, Selection{Mode: "staged"}); e == nil || bytes(t, repo) != before {
		t.Fatal(e)
	}
	put(t, repo, ".gitmodules", "[submodule \"nested\"]\n path = nested\n url = ../nested\n")
	git(t, repo, "add", ".gitmodules")
	p := plan(t, repo, Selection{Mode: "staged"})
	create(t, repo, p)
	git(t, repo, "rm", "--cached", ".gitmodules")
	if _, e := Plan(context.Background(), repo, Selection{Mode: "staged"}); e == nil {
		t.Fatal("orphaned unchanged gitlink")
	}
	git(t, repo, "update-index", "--force-remove", "nested")
	create(t, repo, plan(t, repo, Selection{Mode: "staged"}))
}
func TestInitialCommitEmptyAndNativeLock(t *testing.T) {
	repo := fixture(t)
	p := plan(t, repo, Selection{Mode: "tracked"})
	r, e := Create(context.Background(), repo, Options{Intent: p.Intent, Message: "empty"})
	if e != nil || r.Attempted || !r.Empty {
		t.Fatal(r, e)
	}
	put(t, repo, ".git/index.lock", "another writer")
	if _, e = Plan(context.Background(), repo, Selection{Mode: "staged"}); e == nil {
		t.Fatal("ignored native lock")
	}
	other := t.TempDir()
	git(t, other, "init", "-qb", "initial")
	git(t, other, "config", "user.name", "Fixture")
	git(t, other, "config", "user.email", "fixture@example.invalid")
	put(t, other, "new", "first")
	p = plan(t, other, Selection{Mode: "files", Paths: []string{"new"}})
	if p.Head != "unborn" {
		t.Fatal(p)
	}
	r = create(t, other, p)
	if len(r.Parents) != 0 {
		t.Fatal(r)
	}
}

// +case=A confirmed commit survives an index installation failure; foreign index changes are not overwritten.
func TestIndexRepairDoesNotRetractCommit(t *testing.T) {
	repo := fixture(t)
	put(t, repo, "a", "selected")
	p := plan(t, repo, Selection{Mode: "files", Paths: []string{"a"}})
	hook(t, repo, "post-commit", `printf foreign > "$(git rev-parse --git-dir)/index"`)
	r, e := Create(context.Background(), repo, Options{Intent: p.Intent, Message: "change"})
	if e != nil || !r.Confirmed || r.IndexInstalled || r.RecoveryPath == "" || len(r.Diagnostics) != 1 {
		t.Fatal(r, e)
	}
	if bytes(t, repo) != "foreign" {
		t.Fatal("overwrote foreign index")
	}
	c, e := Check(context.Background(), repo, p.Intent)
	if e != nil || !c.Confirmed || c.Commit != r.Commit {
		t.Fatal(c, e)
	}
}
func TestLinkedCheckoutAndCanceledOperation(t *testing.T) {
	repo := fixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "-qb", "linked", linked)
	put(t, linked, "a", "linked")
	p := plan(t, linked, Selection{Mode: "tracked"})
	r := create(t, linked, p)
	if r.Branch != "linked" || git(t, repo, "show", "HEAD:a") != "base" {
		t.Fatal(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := Create(ctx, linked, Options{Intent: p.Intent, Message: "canceled"})
	if e == nil || r.Attempted {
		t.Fatal(r, e)
	}
}

// +case=Recovery observes missing or different content without authorizing a replay or mutating the index; changing checkout branches is an identity error.
func TestCheckUnconfirmedIntent(t *testing.T) {
	for _, kind := range []string{"unchanged", "different_tree", "different_parent", "different_branch"} {
		t.Run(kind, func(t *testing.T) {
			repo := fixture(t)
			put(t, repo, "a", "planned")
			p := plan(t, repo, Selection{Mode: "files", Paths: []string{"a"}})
			switch kind {
			case "different_tree":
				put(t, repo, "a", "other")
				git(t, repo, "add", "a")
				git(t, repo, "commit", "-qm", "other")
			case "different_parent":
				git(t, repo, "commit", "--allow-empty", "-qm", "intermediate")
				git(t, repo, "add", "a")
				git(t, repo, "commit", "-qm", "same tree, different parent")
			case "different_branch":
				git(t, repo, "checkout", "-qb", "other")
			}
			before := bytes(t, repo)
			tip := git(t, repo, "rev-parse", "HEAD")
			r, e := Check(context.Background(), repo, p.Intent)
			if (e != nil) != (kind == "different_branch") || r.Confirmed || r.Attempted || r.IndexInstalled || bytes(t, repo) != before || git(t, repo, "rev-parse", "HEAD") != tip {
				t.Fatal(r, e)
			}
			if kind == "different_tree" || kind == "different_parent" {
				if r.Commit != tip || r.ActualTree != git(t, repo, "rev-parse", "HEAD^{tree}") {
					t.Fatal(r)
				}
				if kind == "different_parent" && r.ActualTree != p.Tree {
					t.Fatal("fixture must isolate parent mismatch")
				}
			}
		})
	}
}
