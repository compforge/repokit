package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=repocli-test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=repocli-test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(out)
}

func put(t *testing.T, root, name, data string) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCommand(t, dir, "init", "-q", "-b", "main")
	put(t, dir, "source file.ts", "export function a() { return 1; }\nexport function b() { return 2; }\n")
	put(t, dir, "tests/a.test.ts", "import { a as unused } from '../source file';\n")
	put(t, dir, "tests/b.test.ts", "import { b } from '../source file';\n")
	put(t, dir, ".gitignore", "ignored.ts\n")
	gitCommand(t, dir, "add", ".")
	gitCommand(t, dir, "commit", "-qm", "fixture")
	return dir
}

func runJSON(t *testing.T, args []string, input string) repokit.ImpactReport {
	t.Helper()
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), args, strings.NewReader(input), &out, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	var result repokit.ImpactReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	return result
}

func TestWorkingDiffCombinesStagedUnstagedAndUntracked(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "source file.ts", "export function a() { return 3; }\nexport function b() { return 2; }\n")
	gitCommand(t, dir, "add", "source file.ts")
	put(t, dir, "source file.ts", "export function a() { return 4; }\nexport function b() { return 2; }\n")
	put(t, dir, "tests/new.test.ts", "export const added = 1;\n")
	put(t, dir, "ignored.ts", "ignored\n")
	status := gitCommand(t, dir, "status", "--porcelain=v1")
	r := runJSON(t, []string{"impact", "--repo", dir, "--test-dir", "tests", "--json"}, "")
	if r.Scope != "focused" || !reflect.DeepEqual(r.TestFiles, []string{"tests/a.test.ts", "tests/new.test.ts"}) {
		t.Fatalf("result: %+v", r)
	}
	if !reflect.DeepEqual(r.SourceFiles, []string{"source file.ts", "tests/new.test.ts"}) {
		t.Fatalf("source files: %v", r.SourceFiles)
	}
	if got := gitCommand(t, dir, "status", "--porcelain=v1"); got != status {
		t.Fatal("analysis mutated repository")
	}
}

func TestPatchUsesBaseNotWorkingTree(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "source file.ts", "export function a() { return 9; }\nexport function b() { return 2; }\n")
	patch := gitCommand(t, dir, "diff", "--binary", "HEAD")
	gitCommand(t, dir, "restore", "source file.ts")
	// An unrelated dirty worktree must not leak into patch repocli.
	put(t, dir, "source file.ts", "not valid TypeScript {\n")
	r := runJSON(t, []string{"impact", "--repo", dir, "--file", "-", "--test-dir", "tests", "--json"}, patch)
	if r.Input != "patch" || r.Scope != "focused" || !reflect.DeepEqual(r.TestFiles, []string{"tests/a.test.ts"}) {
		t.Fatalf("result: %+v", r)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "source file.ts"))
	if string(data) != "not valid TypeScript {\n" {
		t.Fatal("patch mode wrote worktree")
	}
}

func TestRenameDeletionAndEmptyDiff(t *testing.T) {
	dir := fixture(t)
	r := runJSON(t, []string{"impact", "--repo", dir, "--test-dir", "tests", "--json"}, "")
	if len(r.Changes) != 0 || len(r.SourceFiles) != 0 || len(r.TestFiles) != 0 {
		t.Fatalf("empty diff: %+v", r)
	}
	gitCommand(t, dir, "mv", "source file.ts", "renamed.ts")
	gitCommand(t, dir, "rm", "tests/b.test.ts")
	r = runJSON(t, []string{"impact", "--repo", dir, "--test-dir", "tests", "--json"}, "")
	if !reflect.DeepEqual(r.SourceFiles, []string{"renamed.ts", "tests/b.test.ts"}) {
		t.Fatalf("source files: %v", r.SourceFiles)
	}
	if !reflect.DeepEqual(r.TestFiles, []string{"tests/a.test.ts"}) {
		t.Fatalf("tests: %v", r.TestFiles)
	}
	found := false
	for _, c := range r.Changes {
		if c.Status == "renamed" && c.OldPath == "source file.ts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("changes: %+v", r.Changes)
	}
}

func TestUsageAndErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0}, {[]string{"impact", "--help"}, 0}, {[]string{"unknown"}, 2},
		{[]string{"impact", "--test-dir", "../elsewhere"}, 2}, {[]string{"impact", "--timeout", "0s"}, 2},
		{[]string{"impact", "--repo", t.TempDir()}, 1},
	} {
		var out, err bytes.Buffer
		if code := Execute(context.Background(), tc.args, strings.NewReader(""), &out, &err); code != tc.code {
			t.Fatalf("%v: code %d: %s", tc.args, code, err.String())
		}
	}
}

func TestCancellationDoesNotReturnSuccessfulEmptyResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, stderr bytes.Buffer
	if code := Execute(ctx, []string{"impact", "--repo", fixture(t), "--json"}, strings.NewReader(""), &out, &stderr); code != 1 || stderr.Len() != 0 || string(resultData(t, out.Bytes())) != "null" {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), stderr.String())
	}
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	dir := fixture(t)
	outside := filepath.Join(t.TempDir(), "outside.ts")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.ts")); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "source file.ts", "export function a() { return 3; }\nexport function b() { return 2; }\n")
	r := runJSON(t, []string{"impact", "--repo", dir, "--test-dir", "tests", "--json"}, "")
	if r.Scope != "partial" || len(r.FallbackReasons) == 0 {
		t.Fatalf("result: %+v", r)
	}
}

func TestColocatedSourcesRemainFocused(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "source file.ts", "export function a() { return 3; }\nexport function b() { return 2; }\n")
	for _, dirs := range [][]string{{"."}, {"tests"}, {".", "tests"}} {
		args := []string{"impact", "--repo", dir, "--json"}
		for _, root := range dirs {
			args = append(args, "--test-dir", root)
		}
		r := runJSON(t, args, "")
		if r.Scope != "focused" || !reflect.DeepEqual(r.TestFiles, []string{"tests/a.test.ts"}) {
			t.Fatalf("test dirs %v: %+v", dirs, r)
		}
	}
}

func TestGoImpactBuildsOnlyDiffComponents(t *testing.T) {
	dir := t.TempDir()
	gitCommand(t, dir, "init", "-q", "-b", "main")
	gitCommand(t, dir, "remote", "add", "origin", "https://github.com/example/mono.git")
	put(t, dir, "lib/go.mod", "module example.com/lib\n\ngo 1.25.0\n")
	put(t, dir, "lib/value.go", "package lib\nfunc Value() int { return 1 }\n")
	put(t, dir, "lib/value_test.go", "package lib\nvar _ = Value\n")
	put(t, dir, "app/go.mod", "module example.com/app\n\ngo 1.25.0\n")
	put(t, dir, "app/run.go", "package app\nimport \"example.com/lib\"\nfunc Run() int { return lib.Value() }\n")
	put(t, dir, "app/run_test.go", "package app\nvar _ = Run\n")
	put(t, dir, "worker/go.mod", "module example.com/worker\n\ngo 1.25.0\n")
	put(t, dir, "worker/use.go", "package worker\nimport \"example.com/lib\"\nfunc Run() int { return lib.Value() }\n")
	put(t, dir, "unrelated/go.mod", "module example.com/unrelated\n\ngo 1.25.0\n")
	put(t, dir, "unrelated/other_test.go", "package unrelated\nfunc helper() {}\n")
	gitCommand(t, dir, "add", ".")
	gitCommand(t, dir, "commit", "-qm", "fixture")
	put(t, dir, "lib/value.go", "package lib\nfunc Value() int { return 2 }\n")
	scoped := runJSON(t, []string{"impact", "--repo", dir, "--test-dir", "lib", "--json"}, "")
	if !reflect.DeepEqual(scoped.TestFiles, []string{"lib/value_test.go"}) {
		t.Fatal(scoped)
	}
	r := runJSON(t, []string{"impact", "--repo", dir, "--json"}, "")
	if r.Scope != "focused" || !reflect.DeepEqual(r.SourceFiles, []string{"lib/value.go"}) ||
		!reflect.DeepEqual(r.TestFiles, []string{"lib/value_test.go"}) {
		t.Fatalf("result: %+v", r)
	}
	if len(r.Components) != 4 || r.Repository == nil || r.Repository.Path != "example/mono" {
		t.Fatalf("catalog: %+v", r)
	}
	for _, group := range r.Components {
		if group.Component.Repository != *r.Repository || group.Component.Language != "go" || group.Component.Ecosystem() != "go" {
			t.Fatalf("component metadata: %+v", group)
		}
		switch group.Root {
		case "lib":
			if !reflect.DeepEqual(group.SourceFiles, []string{"lib/value.go"}) || !group.Affected {
				t.Fatal(group)
			}
		case "app", "worker", "unrelated":
			if group.Affected || !group.Complete || len(group.AffectedFiles) != 0 || len(group.SourceFiles) != 0 || len(group.TestFiles) != 0 {
				t.Fatal(group)
			}
		default:
			t.Fatalf("unexpected component: %+v", group)
		}
	}
	put(t, dir, "app/run.go", "package app\nimport \"example.com/lib\"\nfunc Run() int { return lib.Value() + 1 }\n")
	two := runJSON(t, []string{"impact", "--repo", dir, "--test-dir", ".", "--json"}, "")
	if !reflect.DeepEqual(two.TestFiles, []string{"app/run_test.go", "lib/value_test.go"}) {
		t.Fatal(two)
	}
	for _, group := range two.Components {
		if group.Affected != (group.Root == "lib" || group.Root == "app") {
			t.Fatal(group)
		}
	}
}
