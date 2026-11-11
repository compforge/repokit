package state

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSharedCheckoutContract(t *testing.T) {
	data, err := os.ReadFile("../../../conformance/git/checkouts.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Mode     string
		View     string
		Error    bool
		Expected map[string]any
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range cases {
		t.Run(fixture.Name, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{"root": filepath.Join(directory, "repo"), "linked": filepath.Join(directory, " linked\n工作区 "), "metadata": filepath.Join(directory, "metadata"), "missing": filepath.Join(directory, "missing")}
			root := paths["root"]
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			git := func(cwd string, args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %s: %v", args, out, err)
				}
			}
			git(root, "init", "-qb", "main")
			git(root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
			if fixture.Mode == "separate" {
				git(root, "init", "--separate-git-dir", paths["metadata"])
			}
			control := root
			if fixture.Mode == "bare" {
				git(root, "clone", "--bare", root, paths["metadata"])
				control = paths["metadata"]
			}
			git(control, "worktree", "add", "-qb", "topic", paths["linked"], "HEAD")
			view := paths[fixture.View]
			ctx := context.Background()
			info, infoErr := Checkout(ctx, view)
			entries, entriesErr := ListCheckouts(ctx, view)
			if fixture.Error {
				if infoErr == nil || entriesErr == nil {
					t.Fatal("query failure hidden", infoErr, entriesErr)
				}
				return
			}
			if infoErr != nil || entriesErr != nil {
				t.Fatal(infoErr, entriesErr)
			}
			label := func(path string) any {
				if path == "" {
					return nil
				}
				for key, value := range paths {
					if value == path {
						return key
					}
				}
				return path
			}
			rows := make([]any, 0, len(entries))
			for _, entry := range entries {
				rows = append(rows, map[string]any{"path": label(entry.Path), "branch": entry.Branch, "primary": entry.Primary})
			}
			actual := map[string]any{"root": label(info.Root), "mainRoot": label(info.MainRoot), "linked": info.Linked, "entries": rows}
			if !reflect.DeepEqual(actual, fixture.Expected) {
				t.Fatalf("got %#v, want %#v", actual, fixture.Expected)
			}
		})
	}
}
