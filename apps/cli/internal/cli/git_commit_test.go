package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGitCommitCLI(t *testing.T) {
	repo := fixture(t)
	gitCommand(t, repo, "config", "user.name", "Fixture")
	gitCommand(t, repo, "config", "user.email", "fixture@example.invalid")
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(repo, "selected"), []byte("content"), 0600); e != nil {
		t.Fatal(e)
	}
	run := func(args ...string) (int, map[string]any) {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), append(append([]string{"git", "commit"}, args...), "--repo", repo, "--home", home, "--json"), nil, &out, &stderr)
		var r map[string]any
		if e := json.Unmarshal(out.Bytes(), &r); e != nil {
			t.Fatal(e, out.String(), stderr.String())
		}
		return code, r
	}
	code, r := run("create")
	if code != 2 {
		t.Fatal(code, r)
	}
	code, r = run("plan", "--mode", "files", "--file", "selected")
	if code != 0 || r["outcome"] != "succeeded" {
		t.Fatal(code, r)
	}
	p := r["data"].(map[string]any)
	identity := []string{"--branch", p["branch"].(string), "--head", p["head"].(string), "--tree", p["tree"].(string)}
	args := append([]string{"create", "--mode", "files", "--file", "selected", "--index-fingerprint", p["indexFingerprint"].(string), "--message", "selected"}, identity...)
	code, r = run(args...)
	d := r["data"].(map[string]any)
	if code != 0 || d["confirmed"] != true || d["indexInstalled"] != true || d["actualTree"] != p["tree"] {
		t.Fatal(code, r)
	}
	if len(r["cost"].(map[string]any)["phases"].([]any)) < 4 {
		t.Fatal(r)
	}
	code, r = run(append([]string{"check"}, identity...)...)
	if code != 0 || r["data"].(map[string]any)["confirmed"] != true {
		t.Fatal(code, r)
	}
	code, r = run(args...)
	if code == 0 || r["data"].(map[string]any)["attempted"] != false {
		t.Fatal("stale replay", code, r)
	}
}
