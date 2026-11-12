package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	for _, key := range []string{"branch", "head", "tree", "indexFingerprint"} {
		if r["data"].(map[string]any)[key] != p[key] {
			t.Fatalf("stale plan changed %s: %v", key, r)
		}
	}
}

// +case=Text plan exposes every required create argument; text recovery includes actual identity and empty/attempted state without requiring JSON.
func TestGitCommitTextWorkflow(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	gitCommand(t, repo, "config", "user.name", "Fixture")
	gitCommand(t, repo, "config", "user.email", "fixture@example.invalid")
	run := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), append(append([]string{"git", "commit"}, args...), "--repo", repo, "--home", home), nil, &out, &stderr)
		if code != 0 {
			t.Fatal(code, out.String(), stderr.String())
		}
		return out.String()
	}
	empty := run("plan")
	if !strings.Contains(empty, "empty=true attempted=false") {
		t.Fatal(empty)
	}
	if e := os.WriteFile(filepath.Join(repo, "selected"), []byte("content"), 0600); e != nil {
		t.Fatal(e)
	}
	planned := run("plan", "--mode", "files", "--file", "selected")
	field := func(text, key string) string {
		t.Helper()
		match := regexp.MustCompile(`(?:^|\s)` + key + `=([^\s]+)`).FindStringSubmatch(text)
		if len(match) != 2 {
			t.Fatalf("missing %s in %s", key, text)
		}
		return match[1]
	}
	identity := []string{"--branch", field(planned, "branch"), "--head", field(planned, "head"), "--tree", field(planned, "tree")}
	created := run(append([]string{"create", "--mode", "files", "--file", "selected", "--index-fingerprint", field(planned, "indexFingerprint"), "--message", "text workflow"}, identity...)...)
	if !strings.Contains(created, "confirmed=true indexInstalled=true") || field(created, "actualTree") != field(planned, "tree") || !strings.Contains(created, "parents=["+field(planned, "head")+"]") {
		t.Fatal(created)
	}
	var replay, replayErr bytes.Buffer
	replayArgs := append([]string{"git", "commit", "create", "--mode", "files", "--file", "selected", "--index-fingerprint", field(planned, "indexFingerprint"), "--message", "stale"}, identity...)
	code := Execute(context.Background(), append(replayArgs, "--repo", repo, "--home", home), nil, &replay, &replayErr)
	if code == 0 || !strings.Contains(replay.String(), "attempted=false") {
		t.Fatal(code, replay.String(), replayErr.String())
	}
	for _, key := range []string{"branch", "head", "tree", "indexFingerprint"} {
		if field(replay.String(), key) != field(planned, key) {
			t.Fatalf("stale plan changed %s: %s", key, replay.String())
		}
	}
	checked := run(append([]string{"check"}, identity...)...)
	if field(checked, "commit") != field(created, "commit") || !strings.Contains(checked, "attempted=false confirmed=true") {
		t.Fatal(checked)
	}
	gitCommand(t, repo, "commit", "--allow-empty", "-qm", "advanced")
	checked = run(append([]string{"check"}, identity...)...)
	if !strings.Contains(checked, "confirmed=false") || field(checked, "commit") == field(created, "commit") {
		t.Fatal(checked)
	}
}
