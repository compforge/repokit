package worktree

import (
	"context"
	"errors"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/state"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func namedFixture(t *testing.T, separate bool) string {
	t.Helper()
	t.Setenv("REPOKIT_HOME", t.TempDir())
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	args := []string{"init", "-q"}
	if separate {
		args = append(args, "--separate-git-dir", filepath.Join(t.TempDir(), "metadata"))
	}
	runGit := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
	}
	runGit(args...)
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
	return root
}

// +case=Repository aliases, subdirectories and linked checkouts place named worktrees below the same primary checkout.
func TestNamedPlacement(t *testing.T) {
	root := namedFixture(t, false)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	opts := EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: "feature/first", Base: "HEAD"}}
	first, err := Ensure(context.Background(), alias, "first", opts)
	if err != nil || !first.Confirmed || first.Name != "first" || first.Checkout != filepath.Join(root, ".worktrees", "first") {
		t.Fatal(first, err)
	}
	for _, repo := range []string{sub, first.Checkout} {
		reused, err := Ensure(context.Background(), repo, "first", opts)
		if err != nil || reused.Action != "reused" || reused.Checkout != first.Checkout {
			t.Fatal(reused, err)
		}
	}
	opts.Branch = "feature/second"
	second, err := Ensure(context.Background(), first.Checkout, "second", opts)
	if err != nil || second.Checkout != filepath.Join(root, ".worktrees", "second") {
		t.Fatal(second, err)
	}
}

// +case=Invalid names never create directories or branches; an unknown primary checkout never invents a destination.
func TestInvalidPlacement(t *testing.T) {
	root := namedFixture(t, true)
	opts := EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"}}
	for _, name := range []string{"", " ", ".", "..", "../outside", "nested/name", `nested\name`, "/absolute"} {
		r, err := Ensure(context.Background(), root, name, opts)
		var problem *errs.Error
		if !errors.As(err, &problem) || problem.Code != "worktree.invalid_argument" || r.WriteAttempted {
			t.Fatal(name, r, err)
		}
	}
	linked, err := Ensure(context.Background(), root, "first", opts)
	if err != nil {
		t.Fatal(linked, err)
	}
	opts.Branch = "second"
	r, err := Ensure(context.Background(), linked.Checkout, "second", opts)
	var problem *errs.Error
	if !errors.As(err, &problem) || problem.Code != "worktree.primary_unknown" || r.WriteAttempted {
		t.Fatal(r, err)
	}
}
