package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/compforge/repokit/toolkit/go/state"
)

// +case=Ensure JSON carries confirmed identity, explicit action and phase costs; conflicting requests retain a structured failure.
func TestWorktreeEnsureCLI(t *testing.T) {
	root := fixture(t)
	t.Setenv("REPOKIT_HOME", t.TempDir())
	target := filepath.Join(root, "linked")
	for _, want := range []string{"created", "reused"} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"worktree", "ensure", target, "--branch", "topic", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
		var r state.EnsureWorktreeReport
		if e := json.Unmarshal(resultData(t, out.Bytes()), &r); e != nil {
			t.Fatal(e)
		}
		var envelope commandResult
		_ = json.Unmarshal(out.Bytes(), &envelope)
		if code != 0 || !r.Confirmed || r.Action != want || len(envelope.Cost.Phases) == 0 || stderr.Len() != 0 {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"worktree", "ensure", target, "--branch", "different", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
	var envelope commandResult
	_ = json.Unmarshal(out.Bytes(), &envelope)
	if code != 1 || envelope.Error == nil || envelope.Error.Code != "state.worktree_conflict" {
		t.Fatal(code, out.String())
	}
	out.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"worktree", "ensure", target, "--repo", root, "--json"}, nil, &out, &stderr)
	if code != 2 {
		t.Fatal(code, out.String())
	}
}

// +case=Failed preparation returns a partial envelope and preserves a confirmed checkout; no receipt is not ready.
func TestWorktreeEnsurePreparationPartial(t *testing.T) {
	root := fixture(t)
	t.Setenv("REPOKIT_HOME", t.TempDir())
	for name, content := range map[string]string{"package.json": `{"name":"fixture","packageManager":"npm@10"}`, "package-lock.json": `{"lockfileVersion":3}`, ".gitignore": "node_modules/\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "dependencies"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte("#!/bin/sh\necho installer-failed >&2\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := filepath.Join(t.TempDir(), "checkout")
	for _, present := range []bool{false, true} {
		if present {
			if err := os.MkdirAll(filepath.Join(target, "node_modules"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"worktree", "ensure", target, "--branch", "topic", "--base", "HEAD", "--repo", root, "--prepare", "--json"}, nil, &out, &stderr)
		var envelope commandResult
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err, out.String())
		}
		var report worktree.EnsureReport
		if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
			t.Fatal(err)
		}
		if code != 1 || envelope.Outcome != "partial" || !report.Confirmed || report.Ready || report.Preparation == nil {
			t.Fatal(code, out.String(), stderr.String())
		}
		if !present && (envelope.Error == nil || report.Preparation.Bins[0].Stderr != "installer-failed\n") {
			t.Fatal(out.String())
		}
		if present && report.Preparation.Bins[0].Status != "present" {
			t.Fatal(out.String())
		}
	}
}
