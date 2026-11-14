package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/compforge/repokit/toolkit/go/state"
)

func githubFixture(number int, state string) string {
	merged := "null"
	if state == "merged" {
		state = "closed"
		merged = `"2026-01-01T00:00:00Z"`
	}
	return fmt.Sprintf(`{"number":%d,"title":"fixture","state":%q,"merged_at":%s,"html_url":"https://github.com/acme/repo/pull/%d","head":{"sha":"abc","ref":"topic","repo":{"full_name":"acme/repo"}},"base":{"ref":"main","repo":{"full_name":"acme/repo"}}}`, number, state, merged, number)
}
func TestGitHubPaginationSourceIdentityAndMergedState(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("credential missing")
		}
		if r.URL.Query().Get("page") == "1" {
			rows := make([]json.RawMessage, 100)
			for i := range rows {
				rows[i] = json.RawMessage(strings.ReplaceAll(githubFixture(i+1, "merged"), `"full_name":"acme/repo"`, `"full_name":"other/repo"`))
			} // Restore target while retaining a different source.
			for i := range rows {
				rows[i] = json.RawMessage(strings.ReplaceAll(string(rows[i]), `"base":{"ref":"main","repo":{"full_name":"other/repo"}}`, `"base":{"ref":"main","repo":{"full_name":"acme/repo"}}`))
			}
			_ = json.NewEncoder(w).Encode(rows)
			return
		}
		fmt.Fprintf(w, "[%s]", githubFixture(101, "merged"))
	}))
	defer server.Close()
	client, err := NewClient(Config{Host: "github.com", Token: "fixture", APIHost: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.List(context.Background(), "acme/repo", "merged", "acme/repo", "topic")
	if err != nil || len(rows) != 1 || rows[0].Key.Number != 101 || calls != 2 {
		t.Fatal(rows, err, calls)
	}
}
func TestWriteFailureIsUncertainAndNeverRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer server.Close()
	client, _ := NewClient(Config{Host: "github.com", APIHost: server.URL})
	_, err := client.Close(context.Background(), model.Key{Repository: client.Repository("acme/repo"), Number: 1})
	var unknown *errs.Error
	if !errors.As(err, &unknown) || !unknown.Uncertain || unknown.Code != "forge.outcome_unknown" || unknown.HTTPStatus != 503 || calls != 1 {
		t.Fatal(err, calls)
	}
}
func TestGitLabForkCreateAndMergeExpectedSHA(t *testing.T) {
	merged := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.EscapedPath() == "/projects/upstream%2Frepo":
			fmt.Fprint(w, `{"id":9}`)
		case r.Method == "GET" && r.URL.Path == "/projects/8":
			fmt.Fprint(w, `{"path_with_namespace":"fork/repo"}`)
		case r.Method == "PUT" && r.URL.EscapedPath() == "/projects/upstream%2Frepo/merge_requests/2/merge":
			var data map[string]any
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Error(err)
			}
			if data["sha"] != "abc" || data["squash"] != true || data["should_remove_source_branch"] != false {
				t.Error(data)
			}
			merged = true
			fmt.Fprint(w, `{}`)
		case r.Method == "GET" && r.URL.EscapedPath() == "/projects/upstream%2Frepo/merge_requests/2":
			if !merged {
				t.Error("read before merge")
			}
			fmt.Fprint(w, `{"iid":2,"title":"fixture","state":"merged","web_url":"https://gitlab.com/upstream/repo/-/merge_requests/2","sha":"abc","source_branch":"topic","target_branch":"main","source_project_id":8,"target_project_id":9}`)
		case r.Method == "POST":
			if r.URL.EscapedPath() != "/projects/fork%2Frepo/merge_requests" {
				t.Error(r.URL)
			}
			var data map[string]any
			_ = json.NewDecoder(r.Body).Decode(&data)
			if data["target_project_id"] != float64(9) || data["source_branch"] != "topic" {
				t.Error(data)
			}
			fmt.Fprint(w, `{"iid":2,"title":"fixture","state":"opened","web_url":"https://gitlab.com/upstream/repo/-/merge_requests/2","sha":"abc","source_branch":"topic","target_branch":"main","source_project_id":8,"target_project_id":9}`)
		default:
			t.Error(r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{Host: "gitlab.com", APIHost: server.URL})
	pr, err := client.Create(context.Background(), CreateRequest{SourceRepository: "fork/repo", SourceBranch: "topic", TargetRepository: "upstream/repo", TargetBranch: "main", Title: "fixture"})
	if err != nil || pr.Key.Repository.Path != "upstream/repo" || pr.SourceRepository.Path != "fork/repo" {
		t.Fatal(pr, err)
	}
	if _, err = client.Merge(context.Background(), pr.Key, "", "merge"); err == nil {
		t.Fatal("missing expected SHA accepted")
	}
	pr, err = client.Merge(context.Background(), pr.Key, "abc", "squash")
	if err != nil || pr.State != "merged" || !merged {
		t.Fatal(pr, err)
	}
}
func TestRateLimitCooldown(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
	}))
	defer server.Close()
	client, _ := NewClient(Config{Host: "github.com", APIHost: server.URL})
	for i := 0; i < 2; i++ {
		_, err := client.List(context.Background(), "acme/repo", "open", "", "")
		var limited *errs.Error
		if !errors.As(err, &limited) || limited.Code != "forge.rate_limited" || limited.RetryAt == nil || limited.RetryAt.IsZero() {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("cooldown ignored", calls)
	}
}

// +case=PR create, reuse and update preserve an eligible worktree and perform no maintenance queries.
func TestContextPRWritesDoNotMaintainWorktrees(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/repo.git")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "initial")

	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	stale := filepath.Join(t.TempDir(), "stale")
	git("worktree", "add", "-q", "-b", "stale", stale, "HEAD")
	if err := state.SaveWorktreePolicy(context.Background(), root, home, state.WorktreePolicy{}); err != nil {
		t.Fatal(err)
	}
	// Prove the fixture would be reclaimed if PR writes invoked maintenance.
	plan, err := state.CleanWorktrees(context.Background(), root, state.WorktreeCleanOptions{Home: home, DryRun: true})
	if err != nil || len(plan.Errors) != 0 || len(plan.Actions) != 1 || plan.Actions[0].Action != "remove_worktree" || plan.Actions[0].Status != "planned" || plan.Actions[0].Branch != "stale" {
		t.Fatalf("fixture is not eligible for cleanup: %+v %v", plan, err)
	}
	assertRetained := func() {
		t.Helper()
		if _, err := os.Stat(filepath.Join(stale, ".git")); err != nil {
			t.Fatalf("PR write reclaimed worktree: %v", err)
		}
	}
	created := false
	updates := 0
	writes := 0
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && r.URL.Path == "/repos/acme/repo/pulls/1" {
			updates++
			fmt.Fprint(w, githubFixture(1, "open"))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/repos/acme/repo/pulls" {
			created = true
			writes++
			fmt.Fprint(w, githubFixture(1, "open"))
			return
		}
		if r.Method != "GET" || r.URL.Path != "/repos/acme/repo/pulls" || r.URL.Query().Get("state") != "open" || r.URL.Query().Get("head") != "acme:topic" {
			t.Errorf("unexpected maintenance request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reads++
		if !created {
			fmt.Fprint(w, "[]")
		} else {
			fmt.Fprintf(w, "[%s]", githubFixture(1, "open"))
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{Host: "github.com", APIHost: server.URL})
	ctx, err := NewContext(context.Background(), client, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := CreateRequest{SourceBranch: "topic", TargetRepository: "acme/repo", TargetBranch: "main", Title: "fixture"}
	for i := 0; i < 2; i++ {
		result, err := ctx.Ensure(context.Background(), request)
		if err != nil || result.Created != (i == 0) {
			t.Fatal(result, err)
		}
		assertRetained()
	}
	title := "updated"
	if _, err := ctx.Update(context.Background(), model.Key{Repository: client.Repository("acme/repo"), Number: 1}, UpdateRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	assertRetained()
	if writes != 1 || reads != 2 || updates != 1 {
		t.Fatal("unexpected PR requests", writes, reads, updates)
	}
}

func TestProviderHostCaseAndEnterpriseDetection(t *testing.T) {
	for _, test := range []struct{ host, want string }{{"GitHub.com", "github"}, {"GITHUB.example.com", "github"}, {"GITLAB.COM", "gitlab"}, {"Git.example.com", "gitlab"}} {
		provider, err := DetectProvider(test.host, "")
		if err != nil || provider != test.want {
			t.Fatal(test, provider, err)
		}
		client, err := NewClient(Config{Host: test.host})
		if err != nil {
			t.Fatal(err)
		}
		if client.config.Host != strings.ToLower(test.host) {
			t.Fatal(client.config.Host)
		}
	}
}

func TestEnsureSelectsTargetBranchBeforeCheckingAmbiguity(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	git("init", "-q")
	git("remote", "add", "origin", "git@GitHub.com:acme/repo.git")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "initial")
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			created++
			fmt.Fprint(w, strings.ReplaceAll(githubFixture(3, "open"), `"ref":"main"`, `"ref":"next"`))
			return
		}
		fmt.Fprintf(w, "[%s,%s]", githubFixture(1, "open"), strings.ReplaceAll(githubFixture(2, "open"), `"ref":"main"`, `"ref":"release"`))
	}))
	defer server.Close()
	client, _ := NewClient(Config{Host: "GITHUB.COM", APIHost: server.URL})
	contextPR, err := NewContext(context.Background(), client, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"main", "release", "next"} {
		result, err := contextPR.Ensure(context.Background(), CreateRequest{SourceBranch: "topic", TargetRepository: "acme/repo", TargetBranch: base, Title: "fixture"})
		if err != nil || result.PullRequest.TargetBranch != base || result.Created != (base == "next") {
			t.Fatal(base, result, err)
		}
	}
	if created != 1 {
		t.Fatal(created)
	}
}

func TestMalformedListIsNotAnEmptyResult(t *testing.T) {
	for _, body := range []string{"null", "{not json", "{}"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			client, err := NewClient(Config{Host: "github.com", APIHost: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if rows, err := client.List(context.Background(), "acme/repo", "open", "acme/repo", "topic"); err == nil {
				t.Fatal("malformed list accepted", rows)
			}
		})
	}
}

// +case=`Only one exact merged association, verified by numbered GET, authorizes cleanup.`
func TestMergedPullRequestEvidence(t *testing.T) {
	root := t.TempDir()
	out, err := exec.Command("git", "init", "-q", root).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	out, err = exec.Command("git", "-C", root, "remote", "add", "origin", "https://github.com/acme/repo.git").CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	merged := githubFixture(1, "merged")
	for _, test := range []struct {
		name         string
		rows         []string
		verify       string
		secondTarget bool
		want         int
		wantError    bool
	}{
		{name: "merged", rows: []string{merged}, verify: merged, want: 1},
		{name: "closed", rows: []string{githubFixture(1, "closed")}},
		{name: "open", rows: []string{githubFixture(1, "open"), merged}},
		{name: "other head", rows: []string{strings.ReplaceAll(merged, `"sha":"abc"`, `"sha":"other"`)}, verify: strings.ReplaceAll(merged, `"sha":"abc"`, `"sha":"other"`), want: 1},
		{name: "duplicate", rows: []string{merged, githubFixture(2, "merged")}},
		{name: "across targets", rows: []string{merged}, secondTarget: true},
		{name: "changed state", rows: []string{merged}, verify: githubFixture(1, "closed"), wantError: true},
		{name: "changed head", rows: []string{merged}, verify: strings.ReplaceAll(merged, `"sha":"abc"`, `"sha":"other"`), wantError: true},
		{name: "get failed", rows: []string{merged}, wantError: true},
		{name: "other source", rows: []string{strings.Replace(merged, `"full_name":"acme/repo"`, `"full_name":"other/repo"`, 1)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/pulls/1") {
					reads++
					if test.verify == "" {
						w.WriteHeader(503)
					} else {
						fmt.Fprint(w, test.verify)
					}
					return
				}
				body := "[" + strings.Join(test.rows, ",") + "]"
				if strings.Contains(r.URL.Path, "/acme/other/") {
					body = strings.ReplaceAll(body, `"base":{"ref":"main","repo":{"full_name":"acme/repo"}}`, `"base":{"ref":"main","repo":{"full_name":"acme/other"}}`)
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client, err := NewClient(Config{Host: "github.com", APIHost: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			targets := []string{"acme/repo"}
			if test.secondTarget {
				targets = append(targets, "acme/other")
			}
			ctx, err := NewContext(context.Background(), client, root, targets)
			if err != nil {
				t.Fatal(err)
			}
			number, err := ctx.MergedPullRequest(context.Background(), "topic")
			if number != test.want || (err != nil) != test.wantError {
				t.Fatal(number, err)
			}
			if test.want == 1 && reads != 1 {
				t.Fatal("numbered verification skipped")
			}
		})
	}
}
