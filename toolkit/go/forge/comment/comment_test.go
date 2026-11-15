package comment_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

type fixture struct {
	Conversation []map[string]any `json:"conversation"`
	Review       []map[string]any `json:"review"`
	Summaries    []map[string]any `json:"summaries"`
	Threads      []map[string]any `json:"threads"`
	Discussions  []map[string]any `json:"discussions"`
	Expected     []struct {
		ID         string             `json:"id"`
		Parent     *string            `json:"reply_to_id"`
		Resolution comment.Resolution `json:"resolution"`
	} `json:"expected"`
}
type backend struct {
	data      fixture
	provider  string
	calls     []string
	badAck    bool
	failRead  bool
	resolved  bool
	pages     bool
	failLater bool
}

func fixtureFor(t *testing.T, provider string) fixture {
	t.Helper()
	data, e := os.ReadFile("../../../../conformance/forge-comments.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures map[string]fixture
	if e = json.Unmarshal(data, &fixtures); e != nil {
		t.Fatal(e)
	}
	return fixtures[provider]
}
func (b *backend) serve(w http.ResponseWriter, r *http.Request) {
	b.calls = append(b.calls, r.Method+" "+r.URL.Path)
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/graphql" {
		var req struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.HasPrefix(req.Query, "query") {
			threads := b.data.Threads
			for _, t := range threads {
				t["isResolved"] = b.resolved
			}
			write(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": threads, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}}}})
			return
		}
		if b.badAck {
			write(map[string]any{"errors": []any{map[string]string{"message": "failed"}}})
			return
		}
		action := "resolveReviewThread"
		b.resolved = true
		if strings.Contains(req.Query, "unresolveReviewThread") {
			action = "unresolveReviewThread"
			b.resolved = false
		}
		write(map[string]any{"data": map[string]any{action: map[string]any{"thread": map[string]any{"id": "T1", "isResolved": b.resolved}}}})
		return
	}
	if r.Method == "GET" {
		if b.failRead || b.failLater && r.URL.Query().Get("page") == "2" {
			w.WriteHeader(503)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/versions"):
			write([]any{map[string]string{"head_commit_sha": "head", "base_commit_sha": "base", "start_commit_sha": "start"}})
		case strings.HasSuffix(r.URL.Path, "/discussions"):
			write(b.data.Discussions)
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			write(b.data.Summaries)
		case strings.Contains(r.URL.Path, "/issues/"):
			if b.pages && r.URL.Query().Get("page") == "1" {
				rows := []map[string]any{}
				for i := 0; i < 100; i++ {
					rows = append(rows, map[string]any{"id": 100 + i, "body": "page"})
				}
				write(rows)
			} else if b.pages {
				write([]any{})
			} else {
				write(b.data.Conversation)
			}
		case strings.HasSuffix(r.URL.Path, "/comments"):
			write(b.data.Review)
		default:
			write(map[string]any{"number": 7, "state": "open", "html_url": "https://github.com/acme/repo/pull/7"})
		}
		return
	}
	if b.badAck {
		write(map[string]any{})
		return
	}
	if r.Method == "DELETE" {
		w.WriteHeader(204)
		return
	}
	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)
	if resolved, ok := payload["resolved"].(bool); ok {
		d := b.data.Discussions[0]
		for _, n := range d["notes"].([]any) {
			n.(map[string]any)["resolved"] = resolved
		}
		write(d)
		return
	}
	n := map[string]any{"id": 8, "body": payload["body"]}
	if strings.HasSuffix(r.URL.Path, "/comments/1") {
		n["id"] = 1
	}
	if strings.HasSuffix(r.URL.Path, "/notes/2") {
		n["id"] = 2
	}
	if strings.HasSuffix(r.URL.Path, "/replies") {
		n["in_reply_to_id"] = 1
	}
	if b.provider == "gitlab" && strings.HasSuffix(r.URL.Path, "/discussions") {
		write(map[string]any{"id": "new", "individual_note": false, "notes": []any{n}})
		return
	}
	write(n)
}
func setup(t *testing.T, provider string) (comment.Forge, model.Key, *backend) {
	t.Helper()
	b := &backend{provider: provider, data: fixtureFor(t, provider)}
	server := httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(server.Close)
	c, e := forge.NewClient(forge.Config{Host: provider + ".com", APIHost: server.URL, Provider: provider, Token: "fixture", Home: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	return c.Comments(), model.Key{Repository: c.Repository("acme/repo"), Number: 7}, b
}

// +case=Every message is counted, GitHub resource IDs cannot collide, and GitLab discussion membership never fabricates a direct reply.
func TestFlatCommentContract(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			c, key, b := setup(t, provider)
			rows, e := c.List(context.Background(), key)
			if e != nil {
				t.Fatal(e)
			}
			if len(rows) != len(b.data.Expected) {
				t.Fatal(rows)
			}
			for i, r := range rows {
				want := b.data.Expected[i]
				parent := ""
				if want.Parent != nil {
					parent = *want.Parent
				}
				if r.ID != want.ID || r.ReplyToID != parent || r.Resolution != want.Resolution || r.PR != key {
					t.Fatalf("%+v != %+v", r, want)
				}
			}
			if rows[2].Anchor == nil || rows[2].Anchor.Side != "" {
				t.Fatalf("unknown side fabricated: %+v", rows[2].Anchor)
			}
			anchor := rows[0].Anchor
			if provider == "github" {
				anchor = rows[1].Anchor
				if anchor.Line != nil || anchor.OriginalLine == nil || *anchor.OriginalLine != 12 {
					t.Fatal(anchor)
				}
			}
			if anchor == nil || anchor.Side != "LEFT" {
				t.Fatal(anchor)
			}
		})
	}
}

// +case=CRUD returns confirmed identity/body, verifies PR membership and rejects silently retargeted replies or cascading root deletion.
func TestCommentCRUD(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			c, key, _ := setup(t, provider)
			ctx := context.Background()
			created, e := c.Create(ctx, key, "new", nil)
			if e != nil || created.Body != "new" {
				t.Fatal(created, e)
			}
			_, e = c.Create(ctx, key, "positioned", &comment.Anchor{Path: "new.go", OldPath: "old.go", Revision: "head", Side: "LEFT", Line: new(12)})
			if e != nil {
				t.Fatal(e)
			}
			root, reply, target := "review:1", "review:2", "conversation:1"
			if provider == "gitlab" {
				root, reply, target = "note:1", "note:2", "note:2"
			}
			v, e := c.Reply(ctx, key, root, "answer")
			if e != nil || v.Body != "answer" {
				t.Fatal(v, e)
			}
			if provider == "github" && v.ReplyToID != root || provider == "gitlab" && v.ReplyToID != "" {
				t.Fatal(v)
			}
			v, e = c.Update(ctx, key, target, "edited")
			if e != nil || v.ID != target || v.Body != "edited" {
				t.Fatal(v, e)
			}
			d, e := c.Delete(ctx, key, target)
			if e != nil || !d.Deleted {
				t.Fatal(d, e)
			}
			if _, e = c.Delete(ctx, key, root); e == nil {
				t.Fatal("root deletion accepted")
			}
			if _, e = c.Reply(ctx, key, reply, "nested"); e == nil {
				t.Fatal("retargeted nested reply")
			}
			if _, e = c.Get(ctx, key, "foreign:999"); e == nil {
				t.Fatal("accepted foreign ID")
			}
		})
	}
}
func TestCommentResolutionAndUnknownAcknowledgement(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			c, key, b := setup(t, provider)
			id := "review:1"
			if provider == "gitlab" {
				id = "note:1"
			}
			ctx := context.Background()
			v, e := c.Resolve(ctx, key, id, true)
			if e != nil || v.Resolution != comment.Resolved {
				t.Fatal(v, e)
			}
			v, e = c.Resolve(ctx, key, id, false)
			if e != nil || v.Resolution != comment.Unresolved {
				t.Fatal(v, e)
			}
			b.badAck = true
			_, e = c.Resolve(ctx, key, id, true)
			var typed *errs.Error
			if !errors.As(e, &typed) || !typed.Uncertain {
				t.Fatalf("expected unknown outcome: %v", e)
			}
			_, e = c.Create(ctx, key, "body", nil)
			if !errors.As(e, &typed) || !typed.Uncertain {
				t.Fatalf("expected unknown write: %v", e)
			}
		})
	}
}
func TestCommentReadFailuresAndPagination(t *testing.T) {
	c, key, b := setup(t, "github")
	b.pages = true
	rows, e := c.List(context.Background(), key)
	if e != nil || len(rows) != 103 {
		t.Fatal(len(rows), e)
	}
	b.failLater = true
	rows, e = c.List(context.Background(), key)
	if e == nil || rows != nil {
		t.Fatal("partial read became success", rows, e)
	}
	b.failLater = false
	b.pages = false
	b.data.Threads = append(b.data.Threads, b.data.Threads[0])
	if _, e = c.List(context.Background(), key); e == nil {
		t.Fatal("duplicate thread root accepted")
	}
	b.data.Threads = b.data.Threads[:1]
	b.data.Review[1]["in_reply_to_id"] = 999
	if _, e = c.List(context.Background(), key); e == nil {
		t.Fatal("orphan reply disappeared")
	}
}
