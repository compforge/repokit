package state

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// +case=component://docs/home.md
func TestFindRepositories(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	register := func(path string) {
		t.Helper()
		if err := RecordAccess(ctx, path, home); err != nil {
			t.Fatal(err)
		}
	}
	first, git := worktreeFixture(t)
	git("remote", "add", "origin", "git@github.com:example/widget.git")
	linked := filepath.Join(t.TempDir(), "topic")
	git("worktree", "add", "--detach", linked, "HEAD")
	register(linked)
	register(first)
	if _, err := Observe(ctx, first, home, false); err != nil {
		t.Fatal(err)
	}
	second, git2 := worktreeFixture(t)
	git2("remote", "add", "origin", "https://user:secret@github.com/example/widget.git?token=secret")
	register(second)
	third, git3 := worktreeFixture(t)
	git3("remote", "add", "origin", "https://github.com/example/widget-tools.git")
	register(third)
	check := func(query string, paths ...string) {
		t.Helper()
		r, err := FindRepositories(ctx, home, query)
		if err != nil || len(r.Diagnostics) > 0 {
			t.Fatalf("%s: %+v %v", query, r, err)
		}
		actual := []string{}
		for _, m := range r.Matches {
			actual = append(actual, m.Path)
			if m.Path == second && m.Origin != "https://github.com/example/widget.git" {
				t.Fatal("unexpected displayed URL")
			}
		}
		if !slices.Equal(actual, paths) {
			t.Fatalf("%s: got %v; want %v", query, actual, paths)
		}
	}
	two := []string{first, second}
	slices.Sort(two)
	check("WiDgEt", append(slices.Clone(two), third)...)
	check("https://github.com/example/widget", two...)
	check("git@github.com:example/widget.git", two...)
	check(second, second)
	check("unrelated")
	all := []string{first, second, third}
	slices.Sort(all)
	check("example", all...)
	git2("remote", "set-url", "origin", "https://github.com/example/replaced.git")
	check("https://github.com/example/widget", first)
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	check("https://github.com/example/widget")
}

func TestFindSeparateGitDirectory(t *testing.T) {
	root, git := worktreeFixture(t)
	git("init", "--separate-git-dir", filepath.Join(t.TempDir(), "metadata"))
	home := t.TempDir()
	if err := RecordAccess(context.Background(), root, home); err != nil {
		t.Fatal(err)
	}
	report, err := FindRepositories(context.Background(), home, root)
	if err != nil || len(report.Diagnostics) > 0 || len(report.Matches) != 1 || report.Matches[0].Path != root {
		t.Fatalf("%+v %v", report, err)
	}
}
