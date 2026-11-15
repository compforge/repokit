package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

	opts := EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"}, Prepare: true}
	r, e := Ensure(context.Background(), root, "topic", opts)
	if e == nil || !r.Confirmed || r.Ready || r.Preparation == nil || r.Maintenance == nil {
		t.Fatal(r, e)
	}
	if e = os.WriteFile(installer, []byte("#!/bin/sh\nmkdir -p node_modules\n"), 0700); e != nil {
		t.Fatal(e)
	}
	r, e = Ensure(context.Background(), root, "topic", opts)
	if e != nil || !r.Ready || r.Action != "reused" {
		t.Fatal(r, e)
	}
	r, e = Ensure(context.Background(), root, "topic", opts)
	if e != nil || !r.Ready || r.Preparation.Bins[0].Action != "reused" {
		t.Fatal(r, e)
	}
}

func capacityFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
	}
	return root, home
}

func capacityPolicy(t *testing.T, home string, count int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(fmt.Sprintf(`{"worktree":{"max_count":%d,"max_prepared":0}}`, count)), 0600); err != nil {
		t.Fatal(err)
	}
}

func ensureCapacity(t *testing.T, root, name string) EnsureReport {
	t.Helper()
	report, err := Ensure(context.Background(), root, name, EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: name, Base: "HEAD"}, Timeout: 10 * time.Second})
	if err != nil || !report.Confirmed || report.Maintenance == nil || len(report.Diagnostics) > 0 {
		t.Fatalf("%+v %v", report, err)
	}
	return report
}

// +case=Ensure evicts only older excess checkouts in its clone; source and destination remain protected even above the soft limit.
func TestEnsureMaintainsCloneCapacity(t *testing.T) {
	root, home := capacityFixture(t)
	capacityPolicy(t, home, 2)
	old := ensureCapacity(t, root, "old")
	source := ensureCapacity(t, root, "source")
	newest := ensureCapacity(t, source.Checkout, "newest")
	if newest.Maintenance.RemainingCount != 2 {
		t.Fatal(newest.Maintenance)
	}
	if _, err := os.Stat(old.Checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old checkout retained", err)
	}
	for _, path := range []string{source.Checkout, newest.Checkout} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	capacityPolicy(t, home, 0)
	reused := ensureCapacity(t, source.Checkout, "newest")
	if reused.Action != "reused" || reused.Maintenance.RemainingCount != 2 || reused.Maintenance.OverCount != 2 {
		t.Fatal(reused)
	}
}

// +case=Maintenance errors never undo or misreport confirmed creation.
func TestEnsureMaintenanceFailureIsWarning(t *testing.T) {
	root, home := capacityFixture(t)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"worktree":{"max_count":-1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Ensure(context.Background(), root, "topic", EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: "topic", Base: "HEAD"}})
	if err != nil || !report.Confirmed || report.Action != "created" || report.Maintenance == nil || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "worktree.maintenance_incomplete" {
		t.Fatalf("%+v %v", report, err)
	}
	if _, err := os.Stat(report.Checkout); err != nil {
		t.Fatal(err)
	}
}
