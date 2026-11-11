package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

func snapshotJSON(t *testing.T, dir string, extra ...string) repokit.SnapshotReport {
	t.Helper()
	var out, stderr bytes.Buffer
	args := append([]string{"snapshot", "--repo", dir, "--json"}, extra...)
	if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var report repokit.SnapshotReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatalf("%s: %v", out.String(), err)
	}
	if report.SchemaVersion != 2 || !strings.HasPrefix(report.Snapshot, "sha256:") || len(report.Snapshot) != 71 || report.Diagnostics == nil {
		t.Fatalf("invalid report: %+v", report)
	}
	return report
}

func TestSnapshotMatchesDiffAcrossInputs(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "source file.ts", "export function a() { return 7; }\nexport function b() { return 2; }\n")
	gitCommand(t, dir, "add", "source file.ts")
	put(t, dir, "source file.ts", "export function a() { return 8; }\nexport function b() { return 2; }\n")
	put(t, dir, "untracked.txt", "new content\n")
	if err := os.Remove(filepath.Join(dir, "tests/b.test.ts")); err != nil {
		t.Fatal(err)
	}
	status := gitCommand(t, dir, "status", "--porcelain=v1")
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	digests := map[string]bool{}
	for _, tc := range []struct {
		input string
		flags []string
	}{
		{"working_tree", nil}, {"index", []string{"--staged"}}, {"commit", []string{"--head", "HEAD"}},
	} {
		report := snapshotJSON(t, dir, tc.flags...)
		diff := runJSON(t, append([]string{"impact", "--repo", dir, "--json"}, tc.flags...), "")
		if !report.Complete || report.Input != tc.input || report.Snapshot != diff.Snapshot || report.Checkout != diff.Checkout || report.Head != diff.Head {
			t.Fatalf("snapshot=%+v diff=%+v", report, diff)
		}
		digests[report.Snapshot] = true
	}
	if len(digests) != 3 {
		t.Fatal("working/index/commit should have distinct contents")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexBefore, indexAfter) || gitCommand(t, dir, "status", "--porcelain=v1") != status {
		t.Fatal("capture changed repository")
	}
}

func TestSnapshotContentIdentity(t *testing.T) {
	dir := fixture(t)
	initial := snapshotJSON(t, dir)
	staged := snapshotJSON(t, dir, "--staged")
	committed := snapshotJSON(t, dir, "--head", "HEAD")
	if initial.Snapshot != staged.Snapshot || initial.Snapshot != committed.Snapshot {
		t.Fatal("same contents must share identity")
	}
	put(t, dir, "ignored.ts", "ignored content")
	if got := snapshotJSON(t, dir); got.Snapshot != initial.Snapshot {
		t.Fatal("ignored file changed digest")
	}
	// Preparing repository context does not parse source syntax.
	put(t, dir, "broken.go", "this is not Go")
	first := snapshotJSON(t, dir)
	put(t, dir, "broken.go", "different invalid contents")
	second := snapshotJSON(t, dir)
	if !first.Complete || !second.Complete || first.Snapshot == second.Snapshot || first.FileCount != initial.FileCount+1 {
		t.Fatal(first, second)
	}
	if err := os.Remove(filepath.Join(dir, "broken.go")); err != nil {
		t.Fatal(err)
	}
	if got := snapshotJSON(t, dir); got.Snapshot != initial.Snapshot {
		t.Fatal("restored content should restore digest")
	}
}

func TestSnapshotBeforeFirstCommitAndFromWorktree(t *testing.T) {
	empty := t.TempDir()
	gitCommand(t, empty, "init", "-q", "-b", "main")
	report := snapshotJSON(t, empty)
	if !report.Complete || report.FileCount != 0 {
		t.Fatal(report)
	}
	put(t, empty, "new.txt", "untracked before first commit")
	if got := snapshotJSON(t, empty); got.Snapshot == report.Snapshot || got.FileCount != 1 || !got.Complete {
		t.Fatal(got)
	}
	dir := fixture(t)
	wt := filepath.Join(t.TempDir(), "linked")
	gitCommand(t, dir, "worktree", "add", "--detach", wt, "HEAD")
	root := snapshotJSON(t, dir)
	linked := snapshotJSON(t, filepath.Join(wt, "tests"))
	if root.Snapshot != linked.Snapshot || linked.Checkout == root.Checkout {
		t.Fatal(root, linked)
	}
	expected, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Checkout != expected {
		t.Fatalf("checkout=%s want=%s", linked.Checkout, expected)
	}
	put(t, wt, "only-here.txt", "linked worktree content")
	if snapshotJSON(t, wt).Snapshot == root.Snapshot || snapshotJSON(t, dir).Snapshot != root.Snapshot {
		t.Fatal("worktree identities mixed")
	}
}

func TestSnapshotIncompleteEntries(t *testing.T) {
	for _, kind := range []string{"dangling"} {
		t.Run(kind, func(t *testing.T) {
			dir := fixture(t)
			name := "unsupported"
			switch kind {
			case "large":
				put(t, dir, name, strings.Repeat("x", (2<<20)+1))
			default:
				target := "source file.ts"
				if kind == "dangling" {
					target = "does-not-exist"
				}
				if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			gitCommand(t, dir, "add", name)
			gitCommand(t, dir, "commit", "-qm", "unsupported entry")
			for _, flags := range [][]string{nil, {"--staged"}, {"--head", "HEAD"}} {
				report := snapshotJSON(t, dir, flags...)
				if report.Complete || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "snapshot_incomplete" || report.Diagnostics[0].Path != name {
					t.Fatal(report)
				}
			}
		})
	}
}

func TestSnapshotErrorsAndTextOutput(t *testing.T) {
	dir := fixture(t)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--head", "HEAD", "--staged"}, 2}, {[]string{"--timeout", "0s"}, 2},
		{[]string{"extra"}, 2}, {[]string{"--head", "missing-ref"}, 1},
		{[]string{"--repo", t.TempDir()}, 1},
	} {
		var out, stderr bytes.Buffer
		args := append([]string{"snapshot", "--repo", dir, "--json"}, tc.args...)
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != tc.code || stderr.Len() != 0 || string(resultData(t, out.Bytes())) != "null" {
			t.Fatalf("args=%v code=%d out=%s stderr=%s", args, code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"snapshot", "--repo", dir}, nil, &out, &stderr); code != 0 || !strings.Contains(out.String(), "complete=true") {
		t.Fatalf("code=%d %s %s", code, out.String(), stderr.String())
	}
	for _, flags := range [][]string{nil, {"--json"}} {
		if code := Execute(context.Background(), append([]string{"snapshot", "--repo", dir}, flags...), nil, failedWriter{}, &stderr); code != 1 {
			t.Fatalf("output failure exit=%d", code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	stderr.Reset()
	if code := Execute(ctx, []string{"snapshot", "--repo", dir, "--json"}, nil, &out, &stderr); code != 1 || stderr.Len() != 0 || string(resultData(t, out.Bytes())) != "null" {
		t.Fatalf("cancel exit=%d out=%s", code, out.String())
	}
}

func TestSnapshotDetectsChangesBetweenObservations(t *testing.T) {
	dir := fixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := `#!/bin/sh
if [ "$*" = "--no-pager ls-files -z --cached" ]; then
 if [ -f "$SNAPSHOT_TEST_MARKER" ]; then
  printf 'changed during capture' > "$SNAPSHOT_TEST_REPO/source file.ts"
 else
  : > "$SNAPSHOT_TEST_MARKER"
 fi
fi
exec "$SNAPSHOT_TEST_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNAPSHOT_TEST_GIT", realGit)
	t.Setenv("SNAPSHOT_TEST_REPO", dir)
	t.Setenv("SNAPSHOT_TEST_MARKER", filepath.Join(bin, "observed"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	report := snapshotJSON(t, dir)
	if report.Complete || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "snapshot_changed" {
		t.Fatalf("intervening mutation not reported: %+v", report)
	}
}
