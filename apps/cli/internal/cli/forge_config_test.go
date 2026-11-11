package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/state"
)

// +case=PR and both cleanup entries consume the same RepoKit configuration without ambient tokens.
func TestForgeConfigurationAcrossCLIEntries(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN"} {
		t.Setenv(name, "")
	}
	repo, home := fixture(t), t.TempDir()
	gitCommand(t, repo, "remote", "add", "origin", "https://github.com/acme/repo.git")
	if _, err := state.Observe(context.Background(), repo, home, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	contents := []byte(`{"forges":{"github.com":{"token":"fixture-secret"}}}`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("configured credential was not used")
		}
		if r.URL.Path != "/repos/acme/repo/pulls/1" {
			t.Error(r.URL.Path)
		}
		fmt.Fprint(w, `{"number":1,"title":"fixture","state":"open","html_url":"https://github.com/acme/repo/pull/1","head":{"sha":"abc","ref":"topic","repo":{"full_name":"acme/repo"}},"base":{"ref":"main","repo":{"full_name":"acme/repo"}}}`)
	}))
	defer server.Close()
	for _, args := range [][]string{{"pr", "show", "1"}, {"worktree", "clean", "--dry-run"}, {"clean", "--dry-run"}} {
		args = append(args, "--repo", repo, "--home", home, "--api-host", server.URL, "--json")
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, nil, &out, &stderr)
		var envelope commandResult
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err, out.String())
		}
		if code != 0 || envelope.Outcome != "succeeded" {
			t.Fatal(args, code, out.String(), stderr.String())
		}
		if strings.Contains(out.String()+stderr.String(), "fixture-secret") {
			t.Fatal("credential leaked")
		}
	}
	if calls != 1 {
		t.Fatal("PR request was not exercised", calls)
	}
	// Cleanup owns repositories/, never the user configuration next to it.
	if _, err := state.Clean(context.Background(), state.CleanOptions{Home: home, InactiveDays: 30}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, contents) {
		t.Fatal("cleanup changed config", err)
	}
}

func TestForgeConfigurationLoadedOncePerOperation(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.json")
	os.WriteFile(path, []byte(`{"forges":{"git.example.com":{"provider":"gitlab","token":"fixture"}}}`), 0600)
	repo := fixture(t)
	gitCommand(t, repo, "remote", "add", "origin", "https://git.example.com/acme/repo.git")
	var options forgeaccess.Options
	if _, err := options.Context(context.Background(), repo, home); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("invalid"), 0600)
	if _, err := options.Context(context.Background(), repo, home); err != nil {
		t.Fatal("config was reloaded", err)
	}
	var next forgeaccess.Options
	if _, err := next.Context(context.Background(), repo, home); err == nil {
		t.Fatal("new operation ignored invalid config")
	}
}
