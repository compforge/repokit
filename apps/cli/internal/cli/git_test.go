package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// +case=Tag publication exposes confirmed facts, phases and conflict errors through the public CLI envelope
func TestGitTagPublishCLI(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCommand(t, repo, "init", "--bare", remote)
	gitCommand(t, repo, "remote", "add", "origin", remote)
	run := func(target string) (int, map[string]any) {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"git", "tag", "publish", "v1", "component/v1", "--target", target, "--repo", repo, "--home", home, "--json"}, nil, &out, &stderr)
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err, out.String(), stderr.String())
		}
		return code, result
	}
	code, result := run("HEAD")
	if code != 0 || result["outcome"] != "succeeded" || result["data"].(map[string]any)["confirmed"] != true {
		t.Fatal(code, result)
	}
	if len(result["cost"].(map[string]any)["phases"].([]any)) < 4 {
		t.Fatal(result)
	}
	code, result = run("HEAD")
	if code != 0 || result["data"].(map[string]any)["pushAttempted"] != false {
		t.Fatal(code, result)
	}
	gitCommand(t, repo, "commit", "--allow-empty", "-m", "next")
	code, result = run("HEAD")
	if code != 1 || result["outcome"] != "failed" || result["error"].(map[string]any)["code"] != "git.tag_conflict" || result["data"].(map[string]any)["pushAttempted"] != false {
		t.Fatal(code, result)
	}
}
