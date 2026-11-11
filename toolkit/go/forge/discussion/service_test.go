package discussion_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/forge/discussion"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

func testGitHubComment(id, parent int, body string) map[string]any {
	row := map[string]any{"id": id, "body": body, "user": map[string]any{"login": "reviewer"}, "created_at": "2026-01-01T00:00:00Z", "path": "a.go", "original_line": 42}
	if parent > 0 {
		row["in_reply_to_id"] = parent
	}
	return row
}
func testGitHubThreads(id string, resolved bool, root int, next bool, cursor string) map[string]any {
	return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
		"nodes":    []any{map[string]any{"id": id, "isResolved": resolved, "comments": map[string]any{"nodes": []any{map[string]any{"fullDatabaseId": root}}}}},
		"pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor},
	}}}}}
}

// +case=Discussion reads preserve full bodies, replies after the first REST page, and all GraphQL thread pages
func TestDiscussionsGitHubPaginationAndFullEvidence(t *testing.T) {
	body := strings.Repeat("full Markdown\n  indentation\n", 200)
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/graphql":
			var request struct{ Variables map[string]any }
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Variables["cursor"] == nil {
				_ = json.NewEncoder(w).Encode(testGitHubThreads("thread1", false, 1, true, "next"))
			} else {
				_ = json.NewEncoder(w).Encode(testGitHubThreads("thread2", true, 2, false, ""))
			}
		case "/repos/acme/repo/issues/7/comments":
			fmt.Fprint(w, `[{"id":9,"body":"plain"}]`)
		case "/repos/acme/repo/pulls/7/comments":
			if r.URL.Query().Get("page") == "1" {
				rows := []any{testGitHubComment(1, 0, body), testGitHubComment(2, 0, "other thread")}
				for n := 3; n <= 100; n++ {
					rows = append(rows, testGitHubComment(n, 1, "reply"))
				}
				_ = json.NewEncoder(w).Encode(rows)
			} else {
				_ = json.NewEncoder(w).Encode([]any{testGitHubComment(101, 1, "last reply")})
			}
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, _ := forge.NewClient(forge.Config{Host: "github.com", APIHost: server.URL, Token: "fixture"})
	threads, err := client.Discussions().List(context.Background(), model.Key{Repository: client.Repository("acme/repo"), Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]discussion.Discussion{}
	for _, thread := range threads {
		byID[thread.ID] = thread
	}
	if len(threads) != 3 || byID["issue:9"].Replyable || byID["issue:9"].Resolvable == nil || *byID["issue:9"].Resolvable || byID["issue:9"].Resolution != "not_applicable" {
		t.Fatal(threads)
	}
	thread := byID["thread1"]
	if len(thread.Comments) != 100 || thread.Comments[0].Body != body || thread.Comments[99].Body != "last reply" || *thread.Comments[0].Line != 42 || byID["thread2"].Resolution != "resolved" {
		t.Fatal(thread)
	}
	if calls["/graphql"] != 2 || calls["/repos/acme/repo/pulls/7/comments"] != 2 {
		t.Fatal(calls)
	}
}

// +case=A thread ID from another PR or a plain issue comment cannot authorize reply/resolve writes
func TestDiscussionsGitHubWriteMembershipAndResolution(t *testing.T) {
	mutations, replies := 0, 0
	resolved := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/graphql":
			var request struct {
				Query     string
				Variables map[string]any
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if strings.HasPrefix(request.Query, "mutation") {
				mutations++
				resolved = true
				if request.Variables["thread"] != "thread1" {
					t.Fatal("wrong thread")
				}
				fmt.Fprint(w, `{"data":{"resolveReviewThread":{"thread":{"id":"thread1","isResolved":true}}}}`)
			} else {
				_ = json.NewEncoder(w).Encode(testGitHubThreads("thread1", resolved, 1, false, ""))
			}
		case "/repos/acme/repo/issues/7/comments":
			fmt.Fprint(w, `[{"id":9,"body":"plain"}]`)
		case "/repos/acme/repo/pulls/7/comments":
			_ = json.NewEncoder(w).Encode([]any{testGitHubComment(1, 0, "finding")})
		case "/repos/acme/repo/pulls/7/comments/1/replies":
			replies++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(testGitHubComment(10, 1, body["body"]))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, _ := forge.NewClient(forge.Config{Host: "github.com", APIHost: server.URL})
	key := model.Key{Repository: c.Repository("acme/repo"), Number: 7}
	for _, id := range []string{"foreign-thread", "issue:9"} {
		if _, err := c.Discussions().Reply(context.Background(), key, id, "reply"); err == nil {
			t.Fatal("reply accepted", id)
		}
		if _, err := c.Discussions().Resolve(context.Background(), key, id); err == nil {
			t.Fatal("resolve accepted", id)
		}
	}
	if replies != 0 || mutations != 0 {
		t.Fatal(replies, mutations)
	}
	reply, err := c.Discussions().Reply(context.Background(), key, "thread1", "evidence\n  exact")
	if err != nil || reply.Comment.ID != "10" || reply.Comment.Body != "evidence\n  exact" {
		t.Fatal(reply, err)
	}
	for range 2 {
		thread, err := c.Discussions().Resolve(context.Background(), key, "thread1")
		if err != nil || thread.Resolution != "resolved" {
			t.Fatal(thread, err)
		}
	}
	if replies != 1 || mutations != 1 {
		t.Fatal(replies, mutations)
	}
}

func testGitLabDiscussion(id string, resolved bool) map[string]any {
	return map[string]any{"id": id, "individual_note": false, "notes": []any{
		map[string]any{"id": 1, "body": "finding", "resolvable": true, "resolved": resolved, "position": map[string]any{"old_path": "old.go", "old_line": 7}},
		map[string]any{"id": 2, "body": "reply", "resolvable": true, "resolved": resolved},
	}}
}
func TestDiscussionsGitLabLifecycleAndSystemNotes(t *testing.T) {
	resolved := false
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.EscapedPath(), "/projects/group%2Frepo/merge_requests/7/discussions") {
			t.Error(r.URL)
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("PRIVATE-TOKEN") != "fixture" {
			t.Error("missing auth")
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode([]any{testGitLabDiscussion("thread1", resolved), map[string]any{"id": "system", "individual_note": true, "notes": []any{map[string]any{"id": 3, "body": "activity", "system": true}}}})
			return
		}
		writes++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if r.Method == "PUT" {
			resolved = true
			_ = json.NewEncoder(w).Encode(testGitLabDiscussion("thread1", true))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/notes") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 10, "body": payload["body"]})
			return
		}
		_ = json.NewEncoder(w).Encode(testGitLabDiscussion("created", false))
	}))
	defer server.Close()
	c, _ := forge.NewClient(forge.Config{Host: "gitlab.com", APIHost: server.URL, Token: "fixture"})
	key := model.Key{Repository: c.Repository("group/repo"), Number: 7}
	threads, err := c.Discussions().List(context.Background(), key)
	if err != nil || len(threads) != 1 || len(threads[0].Comments) != 2 || threads[0].Comments[0].Path != "old.go" {
		t.Fatal(threads, err)
	}
	created, err := c.Discussions().Create(context.Background(), key, "message")
	if err != nil || created.ID != "created" {
		t.Fatal(created, err)
	}
	reply, err := c.Discussions().Reply(context.Background(), key, "thread1", "reply")
	if err != nil || reply.ThreadID != "thread1" || reply.Comment.ID != "10" {
		t.Fatal(reply, err)
	}
	for range 2 {
		thread, err := c.Discussions().Resolve(context.Background(), key, "thread1")
		if err != nil || thread.Resolution != "resolved" {
			t.Fatal(thread, err)
		}
	}
	if writes != 3 {
		t.Fatal(writes)
	}
}

func TestDiscussionFailureNeverBecomesEmptySuccessOrRetries(t *testing.T) {
	for _, body := range []string{"null", `{"data":null}`, `{"errors":[{"message":"denied"}],"data":{}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			c, _ := forge.NewClient(forge.Config{Host: "github.com", APIHost: server.URL})
			rows, err := c.Discussions().List(context.Background(), model.Key{Repository: c.Repository("acme/repo"), Number: 7})
			if err == nil || rows != nil {
				t.Fatal(rows, err)
			}
			var problem *errs.Error
			if errors.As(err, &problem) && problem.Uncertain {
				t.Fatal("read marked as uncertain write", err)
			}
		})
	}
	for _, response := range []string{`{}`, `{"id":"created","individual_note":false,"notes":[]}`} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, response) }))
		c, _ := forge.NewClient(forge.Config{Host: "gitlab.com", APIHost: server.URL})
		_, err := c.Discussions().Create(context.Background(), model.Key{Repository: c.Repository("group/repo"), Number: 7}, "message")
		server.Close()
		var problem *errs.Error
		if !errors.As(err, &problem) || !problem.Uncertain || calls != 1 {
			t.Fatal(err, calls)
		}
	}
}

func TestDiscussionGraphQLReadFailureAndEnterpriseEndpoint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/graphql" {
			t.Error(r.URL.Path)
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	c, _ := forge.NewClient(forge.Config{Host: "github.example.org", Provider: "github", APIHost: server.URL + "/api/v3"})
	_, err := c.Discussions().List(context.Background(), model.Key{Repository: c.Repository("acme/repo"), Number: 7})
	var problem *errs.Error
	if !errors.As(err, &problem) || problem.Uncertain || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestDiscussionLaterPageFailureReturnsNoPartialList(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(503)
			return
		}
		rows := make([]any, 100)
		for i := range rows {
			rows[i] = testGitLabDiscussion(fmt.Sprintf("thread%d", i), false)
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()
	c, _ := forge.NewClient(forge.Config{Host: "gitlab.com", APIHost: server.URL})
	rows, err := c.Discussions().List(context.Background(), model.Key{Repository: c.Repository("group/repo"), Number: 7})
	if err == nil || rows != nil || calls != 2 {
		t.Fatal(rows, err, calls)
	}
}

func TestDiscussionGitHubCreateRequiresPRAndAcknowledgement(t *testing.T) {
	exists := false
	malformed := false
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if !exists {
				w.WriteHeader(404)
				return
			}
			fmt.Fprint(w, `{"number":7,"title":"fixture","state":"open","html_url":"https://github.com/acme/repo/pull/7","head":{"sha":"abc","ref":"topic","repo":{"full_name":"acme/repo"}},"base":{"ref":"main","repo":{"full_name":"acme/repo"}}}`)
			return
		}
		writes++
		if malformed {
			fmt.Fprint(w, `{}`)
			return
		}
		_ = json.NewEncoder(w).Encode(testGitHubComment(10, 0, "message"))
	}))
	defer server.Close()
	c, _ := forge.NewClient(forge.Config{Host: "github.com", APIHost: server.URL})
	key := model.Key{Repository: c.Repository("acme/repo"), Number: 7}
	if _, err := c.Discussions().Create(context.Background(), key, "message"); err == nil || writes != 0 {
		t.Fatal(err, writes)
	}
	exists = true
	thread, err := c.Discussions().Create(context.Background(), key, "message")
	if err != nil || thread.ID != "issue:10" || thread.Replyable || thread.Resolvable == nil || *thread.Resolvable {
		t.Fatal(thread, err)
	}
	malformed = true
	_, err = c.Discussions().Create(context.Background(), key, "message")
	var problem *errs.Error
	if !errors.As(err, &problem) || !problem.Uncertain || writes != 2 {
		t.Fatal(err, writes)
	}
}

// +case=PR lifecycle and discussion requests share the same rate-limit cooldown in both directions
func TestDiscussionSharesConnectionCooldown(t *testing.T) {
	for _, discussionFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(discussionFirst), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			client, err := forge.NewClient(forge.Config{Host: "github.com", APIHost: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			key := model.Key{Repository: client.Repository("acme/repo"), Number: 7}
			readPR := func() error { _, err := client.Get(context.Background(), key.Repository.Path, key.Number); return err }
			readDiscussion := func() error { _, err := client.Discussions().List(context.Background(), key); return err }
			operations := []func() error{readPR, readDiscussion}
			if discussionFirst {
				operations[0], operations[1] = operations[1], operations[0]
			}
			for _, operation := range operations {
				var problem *errs.Error
				if err := operation(); !errors.As(err, &problem) || problem.Code != "forge.rate_limited" || problem.RetryAt == nil {
					t.Fatalf("expected shared cooldown: %v", err)
				}
			}
			if calls != 1 {
				t.Fatalf("cooldown allowed %d HTTP requests", calls)
			}
		})
	}
}
