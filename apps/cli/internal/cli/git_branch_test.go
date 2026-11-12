package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// +case=Branch CLI exposes confirmed effects, retained backups and phase costs through the common envelope.
func TestGitBranchCLI(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	gitCommand(t, repo, "branch", "-M", "main")
	if e := os.WriteFile(filepath.Join(repo, "new file"), []byte("local"), 0600); e != nil {
		t.Fatal(e)
	}
	run := func(args ...string) (int, map[string]any) {
		var out, stderr bytes.Buffer
		args = append([]string{"git", "branch"}, args...)
		args = append(args, "--repo", repo, "--home", home, "--json")
		code := Execute(context.Background(), args, nil, &out, &stderr)
		var result map[string]any
		if e := json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e, out.String(), stderr.String())
		}
		return code, result
	}
	code, r := run("create", "topic")
	if code != 2 || r["outcome"] != "failed" {
		t.Fatal(code, r)
	}
	code, r = run("create", "topic", "--base", "main")
	if code == 0 || r["outcome"] != "failed" {
		t.Fatal(code, r)
	}
	code, r = run("create", "topic", "--base", "main", "--carry-changes")
	if code != 0 || r["outcome"] != "succeeded" {
		t.Fatal(code, r)
	}
	data := r["data"].(map[string]any)
	stash, ok := data["stashOid"].(string)
	if data["confirmed"] != true || !ok || stash == "" {
		t.Fatal(r)
	}
	if contents, e := os.ReadFile(filepath.Join(repo, "new file")); e != nil || string(contents) != "local" {
		t.Fatal(string(contents), e)
	}
	retained := strings.Fields(gitCommand(t, repo, "stash", "list", "--format=%H"))
	if !slices.Contains(retained, stash) {
		t.Fatal("recovery stash no longer retained", retained, stash)
	}
	if len(r["cost"].(map[string]any)["phases"].([]any)) < 5 {
		t.Fatal(r)
	}
	code, r = run("status", "topic")
	if code != 0 || r["data"].(map[string]any)["writeAttempted"] != false {
		t.Fatal(code, r)
	}
}
