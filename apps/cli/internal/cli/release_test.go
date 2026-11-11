package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// +case=Release CLI publishes and reuses exact content, exposes measured phases and preserves unknown writes in the final envelope.
func TestReleaseCLIWorkflow(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	gitCommand(t, repo, "remote", "add", "origin", "https://github.com/fork/repo.git")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"forges":{"github.com":{"provider":"github","token":"fixture"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const body = "Release notes\n  preserved formatting\n"
	exists, writes, unknown := false, 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes++
			if unknown {
				w.WriteHeader(503)
				return
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["body"] != body || payload["target_commitish"] != sha {
				t.Error(payload)
			}
			exists = true
			w.WriteHeader(201)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/repo/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": sha})
		case "/repos/acme/repo/releases/tags/v1", "/repos/acme/repo/releases/latest":
			if !exists {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1", "name": "v1", "body": body, "target_commitish": "main", "draft": false, "prerelease": false, "html_url": "https://example.test/release"})
		case "/repos/acme/repo/git/ref/tags/v1":
			if !exists {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/tags/v1", "object": map[string]string{"type": "commit", "sha": sha}})
		case "/repos/acme/repo":
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	run := func(text bool, input string, args ...string) (int, map[string]any, string) {
		t.Helper()
		args = append([]string{"release"}, args...)
		args = append(args, "--repo", repo, "--home", home, "--api-host", server.URL, "--target-repository", "acme/repo")
		if !text {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(input), &out, &stderr)
		var result map[string]any
		if !text {
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(out.String(), stderr.String(), err)
			}
		}
		return code, result, out.String()
	}
	code, result, _ := run(false, "", "show", "v1")
	if code != 0 || result["data"].(map[string]any)["release"] != nil {
		t.Fatal(result)
	}
	code, result, _ = run(false, body, "create", "v1", "--target", "main", "--notes-file", "-")
	if code != 0 || result["outcome"] != "succeeded" || result["data"].(map[string]any)["confirmed"] != true || writes != 1 {
		t.Fatal(result)
	}
	if len(result["cost"].(map[string]any)["phases"].([]any)) != 4 {
		t.Fatal(result)
	}
	code, result, _ = run(false, body, "create", "v1", "--target", "main", "--notes-file", "-")
	if code != 0 || result["data"].(map[string]any)["writeAttempted"] != false || writes != 1 {
		t.Fatal(result)
	}
	code, result, _ = run(false, "changed", "create", "v1", "--target", "main", "--notes-file", "-")
	if code != 1 || result["error"].(map[string]any)["code"] != "forge.release_conflict" || writes != 1 {
		t.Fatal(result)
	}
	code, result, _ = run(false, "", "latest")
	if code != 0 || result["data"].(map[string]any)["selection"] != "github_latest_published" {
		t.Fatal(result)
	}
	code, _, output := run(true, "", "show", "v1")
	if code != 0 || !strings.Contains(output, body) || !strings.Contains(output, sha) {
		t.Fatal(output)
	}
	exists, unknown = false, true
	code, result, _ = run(false, body, "create", "v1", "--target", "main", "--notes-file", "-")
	if code != 1 || result["outcome"] != "unknown" || result["error"].(map[string]any)["uncertain"] != true || result["data"].(map[string]any)["writeAttempted"] != true || writes != 2 {
		t.Fatal(result)
	}
	code, result, _ = run(false, body, "create", "v1", "--notes-file", "-")
	if code != 2 || writes != 2 {
		t.Fatal(code, result)
	}
}
