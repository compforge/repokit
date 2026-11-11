package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/forge"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// +case=The public CLI reads full discussion evidence, publishes a reply and resolves the same thread using structured results
func TestPRDiscussionCLIWorkflow(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://gitlab.com/fork/repo.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"forges":{"gitlab.com":{"provider":"gitlab","token":"fixture"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("Long finding\n  indentation\n", 40)
	notes := []map[string]any{{"id": 1, "body": body, "resolvable": true, "resolved": false, "author": map[string]string{"username": "reviewer"}}}
	failWrite := false
	writes := 0
	thread := func() map[string]any { return map[string]any{"id": "thread", "individual_note": false, "notes": notes} }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.EscapedPath(), "/projects/upstream%2Frepo/merge_requests/7/discussions") {
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
			return
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode([]any{thread()})
			return
		}
		writes++
		if failWrite {
			w.WriteHeader(503)
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch r.Method {
		case "POST":
			if strings.HasSuffix(r.URL.Path, "/notes") {
				note := map[string]any{"id": 2, "body": payload["body"], "resolvable": true, "resolved": false}
				notes = append(notes, note)
				_ = json.NewEncoder(w).Encode(note)
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "new", "individual_note": false, "notes": []any{map[string]any{"id": 3, "body": payload["body"]}}})
			}
		case "PUT":
			for _, note := range notes {
				note["resolved"] = true
			}
			_ = json.NewEncoder(w).Encode(thread())
		default:
			t.Error(r.Method)
		}
	}))
	defer server.Close()
	run := func(input string, args ...string) (int, map[string]any, string) {
		t.Helper()
		args = append([]string{"pr", "discussion"}, args...)
		args = append(args, "--repo", repo, "--home", home, "--api-host", server.URL, "--target-repository", "upstream/repo", "--json")
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(input), &out, &stderr)
		var envelope map[string]any
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatalf("%s %s %v", out.String(), stderr.String(), err)
		}
		if envelope["schemaVersion"] != "1.0" || !strings.HasPrefix(envelope["command"].(string), "repocli pr discussion ") {
			t.Fatal(envelope)
		}
		return code, envelope, out.String()
	}
	code, list, _ := run("", "list", "7")
	if code != 0 || len(list["data"].([]any)) != 1 {
		t.Fatal(list)
	}
	code, shown, raw := run("", "show", "7", "thread")
	if code != 0 || shown["data"].(map[string]any)["comments"].([]any)[0].(map[string]any)["body"] != body {
		t.Fatal(raw)
	}
	code, reply, _ := run("evidence\n  exact\n", "reply", "7", "thread", "--body-file", "-")
	if code != 0 || reply["data"].(map[string]any)["comment"].(map[string]any)["body"] != "evidence\n  exact\n" {
		t.Fatal(reply)
	}
	code, resolved, _ := run("", "resolve", "7", "thread")
	if code != 0 || resolved["data"].(map[string]any)["resolution"] != "resolved" {
		t.Fatal(resolved)
	}
	if code, _, _ := run("", "resolve", "7", "thread"); code != 0 || writes != 2 {
		t.Fatal(code, writes)
	}
	code, created, _ := run("new note", "create", "7", "--body-file", "-")
	if code != 0 || created["data"].(map[string]any)["id"] != "new" {
		t.Fatal(created)
	}
	failWrite = true
	code, unknown, _ := run("another", "reply", "7", "thread", "--body-file", "-")
	if code != 1 || unknown["outcome"] != "unknown" || unknown["error"].(map[string]any)["uncertain"] != true || writes != 4 {
		t.Fatal(unknown, writes)
	}
	code, missing, _ := run("", "show", "7", "foreign")
	if code != 1 || missing["error"].(map[string]any)["code"] != "forge.not_found" {
		t.Fatal(missing)
	}

}

func TestPRDiscussionArgumentsAndFullText(t *testing.T) {
	for _, args := range [][]string{{"list", "0"}, {"create", "7"}, {"reply", "7", "thread", "--body-file", "-"}, {"show", "7"}, {"resolve", "7", ""}} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), append([]string{"pr", "discussion"}, append(args, "--json")...), strings.NewReader("  \n"), &out, &stderr)
		if code != 2 || !strings.Contains(out.String(), `"outcome": "failed"`) {
			t.Fatalf("%v: %d %s %s", args, code, out.String(), stderr.String())
		}
	}
}

func TestDiscussionTextPreservesBodiesAndReplies(t *testing.T) {
	// Presentation uses the same public objects as JSON, without summary-length limits.
	body := strings.Repeat("Markdown\n  indentation\n", 100)
	var out bytes.Buffer
	displayDiscussion(&out, forge.Discussion{ID: "thread", Resolution: "unresolved", Comments: []forge.Comment{{ID: "1", Body: body, Path: "a.go", Line: new(42)}, {ID: "2", Body: "last reply"}}}, true)
	if !strings.Contains(out.String(), body) || !strings.Contains(out.String(), "last reply") || !strings.Contains(out.String(), "a.go:42") {
		t.Fatal(out.String())
	}
}
