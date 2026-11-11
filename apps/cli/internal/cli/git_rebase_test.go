package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// +case=Rebase exposes lifecycle phases, costs, exact identity and argument errors in the shared CLI contract.
func TestGitRebaseCLI(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCommand(t, repo, "branch", "-M", "main")
	gitCommand(t, repo, "init", "--bare", remote)
	gitCommand(t, repo, "remote", "add", "origin", remote)
	gitCommand(t, repo, "push", "origin", "main")
	gitCommand(t, repo, "checkout", "-b", "feature")
	gitCommand(t, repo, "commit", "--allow-empty", "-m", "feature")
	gitCommand(t, repo, "push", "origin", "feature")
	run := func(args ...string) (int, map[string]any) {
		var out, stderr bytes.Buffer
		args = append([]string{"git", "rebase"}, args...)
		args = append(args, "--repo", repo, "--home", home, "--json")
		code := Execute(context.Background(), args, nil, &out, &stderr)
		var result map[string]any
		if e := json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e, out.String(), stderr.String())
		}
		return code, result
	}
	code, result := run("start")
	if code != 2 || result["outcome"] != "failed" {
		t.Fatal(code, result)
	}
	code, result = run("start", "--branch", "feature", "--target", "main")
	if code != 0 || result["outcome"] != "succeeded" || result["data"].(map[string]any)["phase"] != "ready" {
		t.Fatal(code, result)
	}
	if len(result["cost"].(map[string]any)["phases"].([]any)) < 4 {
		t.Fatal(result)
	}
	code, result = run("finish")
	data := result["data"].(map[string]any)
	if code != 0 || data["confirmed"] != true || data["phase"] != "published" {
		t.Fatal(code, result)
	}
	code, result = run("finish")
	if code != 0 || result["data"].(map[string]any)["pushAttempted"] != false {
		t.Fatal(code, result)
	}
}
