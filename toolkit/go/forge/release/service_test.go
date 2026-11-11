package release_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/forge/release"
)

const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const otherSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const tag = "toolkit/v1.0.0"
const notes = "Full notes\n  indentation\n"

type remote struct {
	mu                                     sync.Mutex
	provider                               string
	postReadStatus                         int
	missingState, loopTags                 bool
	exists                                 bool
	commit                                 string
	name, body                             string
	draft, annotated                       bool
	status, readStatus, writes, resolves   int
	missingAfterWrite, malformed, wrongTag bool
	race                                   bool
}

func (f *remote) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.provider == "github" && r.Header.Get("Authorization") != "Bearer fixture" || f.provider == "gitlab" && r.Header.Get("PRIVATE-TOKEN") != "fixture" {
			t.Error("missing configured authentication")
		}
		path := r.URL.Path
		encode := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		if r.Method == "POST" {
			f.writes++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			targetKey, notesKey := "target_commitish", "body"
			if f.provider == "gitlab" {
				targetKey, notesKey = "ref", "description"
			}
			if payload[targetKey] != sha || payload["tag_name"] != tag {
				t.Errorf("target not pinned: %v", payload)
			}
			if !f.missingAfterWrite {
				f.exists, f.commit = true, sha
				f.name, _ = payload["name"].(string)
				f.body, _ = payload[notesKey].(string)
				if f.race {
					f.commit = otherSHA
				}
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
			} else {
				w.WriteHeader(201)
			}
			encode(map[string]any{})
			return
		}
		if f.writes > 0 && f.postReadStatus != 0 {
			w.WriteHeader(f.postReadStatus)
			return
		}
		if f.readStatus != 0 {
			w.WriteHeader(f.readStatus)
			return
		}
		if strings.Contains(path, "/commits/") {
			f.resolves++
			// A moving branch must never be resolved again after publication starts.
			commit := sha
			if f.resolves > 1 {
				commit = otherSHA
			}
			encode(map[string]any{"sha": commit, "id": commit})
			return
		}
		if strings.Contains(path, "/releases/") {
			if !f.exists {
				w.WriteHeader(404)
				return
			}
			if f.malformed {
				encode(map[string]any{})
				return
			}
			observedTag := tag
			if f.wrongTag {
				observedTag = "wrong"
			}
			row := map[string]any{"tag_name": observedTag, "name": f.name, "body": f.body, "description": f.body, "target_commitish": "moving-branch", "draft": f.draft, "prerelease": false, "released_at": "2026-01-01T00:00:00Z", "upcoming_release": f.draft, "html_url": "https://example.test/release", "_links": map[string]string{"self": "https://example.test/release"}}
			if f.missingState {
				delete(row, "draft")
				delete(row, "released_at")
			}
			encode(row)
			return
		}
		if strings.Contains(path, "/git/ref/tags/") || strings.Contains(path, "/repository/tags/") {
			if f.commit == "" {
				w.WriteHeader(404)
				return
			}
			objectType := "commit"
			if f.annotated {
				objectType = "tag"
			}
			encode(map[string]any{"ref": "refs/tags/" + tag, "name": tag, "object": map[string]string{"type": objectType, "sha": f.commit}, "commit": map[string]string{"id": f.commit}})
			return
		}
		if strings.Contains(path, "/git/tags/") {
			kind := "commit"
			if f.loopTags {
				kind = "tag"
			}
			encode(map[string]any{"object": map[string]string{"type": kind, "sha": f.commit}})
			return
		}
		if path == "/repos/acme/repo" || path == "/projects/acme/repo" {
			encode(map[string]any{"id": 1})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	}
}
func setup(t *testing.T, f *remote, transport http.RoundTripper) *forge.Client {
	t.Helper()
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	client := &http.Client{Transport: transport}
	c, err := forge.NewClient(forge.Config{Host: f.provider + ".com", Provider: f.provider, APIHost: server.URL, Token: "fixture", Home: t.TempDir(), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func request() release.CreateRequest {
	return release.CreateRequest{Tag: tag, Target: "main", Name: "Release", Notes: notes}
}

// +case=Both providers confirm exact tag, commit and full notes, reuse identical releases, and never overwrite conflicts.
func TestCreate(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		for _, tc := range []struct {
			name      string
			configure func(*remote)
			code      string
			confirmed bool
			writes    int
		}{
			{"new", func(f *remote) {}, "", true, 1},
			{"verification unavailable", func(f *remote) { f.postReadStatus = 503 }, "forge.release_outcome_unknown", false, 1},
			{"missing publication state", func(f *remote) { f.exists = true; f.commit = sha; f.missingState = true }, "forge.release_invalid_response", false, 0},
			{"existing", func(f *remote) { f.exists = true; f.commit = sha }, "", true, 0},
			{"existing annotated tag", func(f *remote) { f.commit = sha; f.annotated = true }, "", true, 1},
			{"tag conflict", func(f *remote) { f.commit = otherSHA }, "forge.release_conflict", false, 0},
			{"notes conflict", func(f *remote) { f.exists = true; f.commit = sha; f.body = "different" }, "forge.release_conflict", false, 0},
			{"draft conflict", func(f *remote) { f.exists = true; f.commit = sha; f.draft = true }, "forge.release_conflict", false, 0},
			{"recover server failure", func(f *remote) { f.status = 503 }, "", true, 1},
			{"concurrent identical creation", func(f *remote) { f.status = 422 }, "", true, 1},
			{"unconfirmed write", func(f *remote) { f.status = 503; f.missingAfterWrite = true }, "forge.release_outcome_unknown", false, 1},
			{"acknowledged but absent", func(f *remote) { f.missingAfterWrite = true }, "forge.release_outcome_unknown", false, 1},
			{"rejected write", func(f *remote) { f.status = 403; f.missingAfterWrite = true }, "forge.access_denied", false, 1},
			{"racing tag", func(f *remote) { f.race = true }, "forge.release_conflict", false, 1},
			{"authentication", func(f *remote) { f.readStatus = 401 }, "forge.authentication_required", false, 0},
			{"hidden repository", func(f *remote) { f.readStatus = 404 }, "forge.not_found", false, 0},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				f := &remote{provider: provider, name: "Release", body: notes}
				tc.configure(f)
				c := setup(t, f, nil)
				result, err := c.Releases().Create(context.Background(), c.Repository("acme/repo"), request())
				var p *errs.Error
				if tc.code == "" && err != nil || tc.code != "" && (!errors.As(err, &p) || p.Code != tc.code) {
					t.Fatalf("%+v %v", result, err)
				}
				if result.Confirmed != tc.confirmed || f.writes != tc.writes || result.WriteAttempted != (tc.writes > 0) {
					t.Fatalf("%+v writes=%d", result, f.writes)
				}
				if p != nil && p.Uncertain != (tc.code == "forge.release_outcome_unknown") {
					t.Fatalf("%+v", p)
				}
				if tc.confirmed && (result.Release.Notes != notes || result.Release.TargetCommit != sha || result.TargetCommit != sha) {
					t.Fatal(result)
				}
				if tc.writes > 0 && len(result.Phases) != 4 {
					t.Fatal(result.Phases)
				}
			})
		}
	}
}

func TestQueries(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			f := &remote{provider: provider, name: "Release", body: notes}
			c := setup(t, f, nil)
			repo := c.Repository("acme/repo")
			result, err := c.Releases().Show(context.Background(), repo, tag)
			if err != nil || result.Release != nil {
				t.Fatal(result, err)
			}
			f.exists, f.commit, f.annotated = true, sha, true
			result, err = c.Releases().Latest(context.Background(), repo)
			if err != nil || result.Release.TargetCommit != sha || result.Release.Notes != notes || !strings.HasPrefix(result.Selection, provider) {
				t.Fatal(result, err)
			}
			f.wrongTag = true
			if _, err = c.Releases().Show(context.Background(), repo, tag); err == nil {
				t.Fatal("accepted mismatched tag")
			}
			f.wrongTag = false
			f.malformed = true
			if _, err = c.Releases().Latest(context.Background(), repo); err == nil {
				t.Fatal("accepted malformed response")
			}
			f.readStatus = 404
			if _, err = c.Releases().Latest(context.Background(), repo); err == nil {
				t.Fatal("hidden repository treated as no release")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// +case=A write exhausting its reserved budget can succeed by live reconciliation; caller cancellation never receives detached verification.
func TestTimeoutRecoveryAndCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "write timeout", true: "caller canceled"}[cancelCaller], func(t *testing.T) {
			f := &remote{provider: "github", name: "Release", body: notes}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" {
					return http.DefaultTransport.RoundTrip(r)
				}
				f.mu.Lock()
				f.exists, f.commit = true, sha
				f.writes++
				f.mu.Unlock()
				if cancelCaller {
					cancel()
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			c := setup(t, f, transport)
			result, err := c.Releases().Create(ctx, c.Repository("acme/repo"), request())
			if cancelCaller {
				var p *errs.Error
				if !errors.As(err, &p) || !p.Uncertain || result.Confirmed {
					t.Fatal(result, err)
				}
			} else if err != nil || !result.Confirmed {
				t.Fatal(result, err)
			}
			if f.writes != 1 {
				t.Fatal(f.writes)
			}
		})
	}
}

func TestInvalidInputsNeverWrite(t *testing.T) {
	f := &remote{provider: "github"}
	c := setup(t, f, nil)
	for _, name := range []string{"", "../x", "a..b", "a.lock", "a//b", "x\n", "a@{b", "x:y"} {
		req := request()
		req.Tag = name
		if _, err := c.Releases().Create(context.Background(), c.Repository("acme/repo"), req); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	req := request()
	req.Target = ""
	if _, err := c.Releases().Create(context.Background(), c.Repository("acme/repo"), req); err == nil {
		t.Fatal("accepted missing target")
	}
	if f.writes != 0 || f.resolves != 0 {
		t.Fatal(f)
	}
}

// +case=Malformed annotated tag cycles are bounded and never permit Release creation.
func TestAnnotatedTagDepthBound(t *testing.T) {
	f := &remote{provider: "github", commit: sha, annotated: true, loopTags: true}
	c := setup(t, f, nil)
	result, err := c.Releases().Create(context.Background(), c.Repository("acme/repo"), request())
	var p *errs.Error
	if !errors.As(err, &p) || p.Code != "forge.release_invalid_response" || result.WriteAttempted || f.writes != 0 {
		t.Fatal(result, err)
	}
}
