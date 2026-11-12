package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/compforge/repokit/toolkit/go/state"
)

func TestEnsurePreparationAndRecovery(t *testing.T) {
	t.Setenv("REPOKIT_HOME", t.TempDir())
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatal(string(out), e)
		}
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	for name, data := range map[string]string{".gitignore": "node_modules/\n", "package.json": `{"name":"fixture","packageManager":"npm@10.0.0"}`, "package-lock.json": `{"lockfileVersion":3}`} {
		if e := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	git("add", ".")
	git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	bin := t.TempDir()
	installer := filepath.Join(bin, "npm")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if e := os.WriteFile(installer, []byte("#!/bin/sh\nexit 3\n"), 0700); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "checkout")
	opts := EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"}, Prepare: true}
	r, e := Ensure(context.Background(), root, target, opts)
	if e == nil || !r.Confirmed || r.Ready || r.Preparation == nil {
		t.Fatal(r, e)
	}
	if e = os.WriteFile(installer, []byte("#!/bin/sh\nmkdir -p node_modules\n"), 0700); e != nil {
		t.Fatal(e)
	}
	r, e = Ensure(context.Background(), root, target, opts)
	if e != nil || !r.Ready || r.Action != "reused" {
		t.Fatal(r, e)
	}
	r, e = Ensure(context.Background(), root, target, opts)
	if e != nil || !r.Ready || r.Preparation.Bins[0].Action != "reused" {
		t.Fatal(r, e)
	}
}
