package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// +case=Push/check share the CLI envelope, explicit frozen identity, read-only absence and measured phase costs.
func TestGitPushCLI(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCommand(t, repo, "init", "--bare", remote)
	gitCommand(t, repo, "remote", "add", "origin", remote)
	sha := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	run := func(args ...string) (int, map[string]any) {
		var out, stderr bytes.Buffer
		args = append([]string{"git", "push"}, args...)
		args = append(args, "--repo", repo, "--home", home, "--json")
		code := Execute(context.Background(), args, nil, &out, &stderr)
		var r map[string]any
		if e := json.Unmarshal(out.Bytes(), &r); e != nil {
			t.Fatal(e, out.String(), stderr.String())
		}
		return code, r
	}
	code, r := run("--branch", "main")
	if code != 2 || r["outcome"] != "failed" {
		t.Fatal(code, r)
	}
	code, r = run("--branch", "main", "--commit", sha, "--check")
	if code != 0 || r["outcome"] != "succeeded" || r["data"].(map[string]any)["confirmed"] != false || r["data"].(map[string]any)["remoteState"] != "missing" {
		t.Fatal(code, r)
	}
	code, r = run("--branch", "main", "--commit", sha, "--set-upstream")
	data := r["data"].(map[string]any)
	if code != 0 || data["confirmed"] != true || data["remoteCommit"] != sha || data["upstreamConfirmed"] != true || len(r["cost"].(map[string]any)["phases"].([]any)) < 5 {
		t.Fatal(code, r)
	}
	code, r = run("--branch", "main", "--commit", sha)
	if code != 0 || r["data"].(map[string]any)["pushAttempted"] != false {
		t.Fatal(code, r)
	}
}
