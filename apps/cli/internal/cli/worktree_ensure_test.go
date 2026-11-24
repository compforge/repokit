package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// +case=Ensure JSON carries confirmed identity, explicit action and phase costs; conflicting requests retain a structured failure.
func TestWorktreeEnsureCLI(t *testing.T) {
	root, err := filepath.EvalSymlinks(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPOKIT_HOME", t.TempDir())
	target := filepath.Join(root, ".worktrees", "topic")
	for _, want := range []string{"created", "reused"} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"worktree", "ensure", "topic", "--branch", "topic", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
		var r worktree.EnsureReport
		if e := json.Unmarshal(resultData(t, out.Bytes()), &r); e != nil {
			t.Fatal(e)
		}
		var envelope commandResult
		_ = json.Unmarshal(out.Bytes(), &envelope)
		if code != 0 || !r.Confirmed || r.Name != "topic" || r.Checkout != target || r.Action != want || r.Maintenance == nil || len(envelope.Cost.Phases) == 0 || stderr.Len() != 0 {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"worktree", "ensure", "topic", "--branch", "different", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
	var envelope commandResult
	_ = json.Unmarshal(out.Bytes(), &envelope)
	if code != 1 || envelope.Error == nil || envelope.Error.Code != "state.worktree_conflict" {
		t.Fatal(code, out.String())
	}
	out.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"worktree", "ensure", "topic", "--repo", root, "--json"}, nil, &out, &stderr)
	if code != 2 {
		t.Fatal(code, out.String())
	}
}

// +case=Failed preparation returns a partial envelope and preserves a confirmed checkout; no receipt is not ready.
func TestWorktreeEnsurePreparationPartial(t *testing.T) {
	root := fixture(t)
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", filepath.Join(home, ".repokit"))
	export := filepath.Join(t.TempDir(), "result.json")
	for name, content := range map[string]string{"package.json": `{"name":"fixture","packageManager":"bun@1.3.0"}`, "bun.lock": "{}", ".gitignore": "node_modules/\n"} {
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
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte("#!/bin/sh\necho 'error: Workspace not found \"fixture-package\"' >&2\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := filepath.Join(root, ".worktrees", "topic")
	for _, present := range []bool{false, true} {
		if present {
			if err := os.MkdirAll(filepath.Join(target, "node_modules"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"worktree", "ensure", "topic", "--branch", "topic", "--base", "HEAD", "--repo", root, "--prepare", "--json", "--output", export}, nil, &out, &stderr)
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
		if !present && (envelope.Error == nil || report.Preparation.Bins[0].Stderr != "error: Workspace not found \"fixture-package\"\n") {
			t.Fatal(out.String())
		}
		logs := readLogs(t, home)
		for _, want := range []string{"msg=worktree.bin_not_ready", "bin=node_modules", "confirmed=true", "ready=false", "result_path=" + export} {
			if !strings.Contains(logs, want) {
				t.Fatalf("missing %q in %s", want, logs)
			}
		}
		if strings.Contains(logs, "fixture-package") {
			t.Fatal("raw installer stderr in command log")
		}
		if !present {
			saved, err := os.ReadFile(export)
			if err != nil || !bytes.Equal(saved, out.Bytes()) || !strings.Contains(logs, "error_code=depot.install_failed installer_exit_code=3 installer_cause=workspace_not_found") {
				t.Fatalf("failed installation evidence: %v, %s", err, logs)
			}
		}
		if present && report.Preparation.Bins[0].Status != "present" {
			t.Fatal(out.String())
		}
	}
}

// +case=Named ensure requires explicit repository selection and rejects the removed path override.
func TestWorktreeEnsureRequiresRepository(t *testing.T) {
	for _, args := range [][]string{
		{"worktree", "ensure", "topic", "--branch", "topic", "--base", "HEAD"},
		{"worktree", "ensure", "topic", "--repo", ".", "--branch", "topic", "--base", "HEAD", "--path", "/tmp/override"},
	} {
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 2 {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
}

// +case=An invalid maintenance policy emits warnings while ensure remains successful and confirmed.
func TestWorktreeEnsureMaintenanceWarning(t *testing.T) {
	root := fixture(t)
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"worktree":{"max_count":-1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"worktree", "ensure", "topic", "--branch", "topic", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
	var envelope commandResult
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err, out.String())
	}
	var report worktree.EnsureReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if code != 0 || envelope.Outcome != "succeeded" || !report.Confirmed || len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Severity != "warning" {
		t.Fatal(code, out.String(), stderr.String())
	}
}

// +case=Selecting the primary repository from an older linked checkout still protects the invoking checkout.
func TestWorktreeEnsureProtectsInvokingCheckout(t *testing.T) {
	root := fixture(t)
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	old, err := worktree.Ensure(context.Background(), root, "old", worktree.EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: "old", Base: "HEAD"}})
	if err != nil || !old.Confirmed {
		t.Fatal(old, err)
	}
	t.Chdir(old.Checkout)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"worktree":{"max_count":0,"max_prepared":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"worktree", "ensure", "new", "--branch", "new", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
	var report worktree.EnsureReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if code != 0 || !report.Confirmed || report.Maintenance == nil || report.Maintenance.RemainingCount != 2 {
		t.Fatal(code, out.String(), stderr.String())
	}
	if _, err := os.Stat(old.Checkout); err != nil {
		t.Fatal("invoking checkout removed", err)
	}
}
