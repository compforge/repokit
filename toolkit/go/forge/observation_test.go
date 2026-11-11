package forge

import (
	"context"
	"encoding/json"
	"fmt"
	repostate "github.com/compforge/repokit/toolkit/go/state"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadFreshnessConnectionScopeAndTerminalLifecycle(t *testing.T) {
	calls := 0
	state := "open"
	offline := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if offline {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, githubFixture(1, state))
	}))
	defer server.Close()
	config := Config{Host: "github.com", Token: "account-a", APIHost: server.URL, Home: t.TempDir()}
	client, _ := NewClient(config)
	ctx := context.Background()
	if _, err := client.Load(ctx, "acme/repo", 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Load(ctx, "acme/repo", 1, time.Hour); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	config.Token = "account-b"
	other, _ := NewClient(config)
	if _, err := other.Load(ctx, "acme/repo", 1, time.Hour); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	state = "merged"
	if _, err := other.Load(ctx, "acme/repo", 1, 0); err != nil {
		t.Fatal(err)
	}
	state = "open"
	if _, err := other.Load(ctx, "acme/repo", 1, 0); err == nil {
		t.Fatal("merged lifecycle regressed")
	}
	row, err := other.Load(ctx, "acme/repo", 1, time.Hour)
	if err != nil || row.State != "merged" {
		t.Fatal(row, err)
	}
	offline = true
	if _, err := other.Load(ctx, "acme/repo", 1, 0); err == nil {
		t.Fatal("failed refresh returned stale success")
	}
}

func TestConnectionIdentityConformance(t *testing.T) {
	data, err := os.ReadFile("../../../conformance/repository-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Connections []struct{ Host, Provider, GoAPIHost, Token, Connection string }
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Connections {
		client, err := NewClient(Config{Host: row.Host, Provider: row.Provider, APIHost: row.GoAPIHost, Token: row.Token})
		if err != nil {
			t.Fatal(err)
		}
		_, _, connection := client.observationLocation(Key{client.Repository("owner/project"), 1})
		if connection != row.Connection {
			t.Fatalf("connection mismatch for %s: %s", row.Host, connection)
		}
	}
}
func TestMutationsPublishCacheAndKeepDiscoveryScopes(t *testing.T) {
	state, title := "open", "original"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if value, ok := payload["title"]; ok {
				title = value
			}
			if value, ok := payload["state"]; ok {
				state = value
			}
		}
		if r.Method == "PUT" {
			state = "merged"
			fmt.Fprint(w, `{"merged":true}`)
			return
		}
		fmt.Fprint(w, strings.Replace(githubFixture(1, state), `"title":"fixture"`, `"title":`+strconv.Quote(title), 1))
	}))
	defer server.Close()
	config := Config{Host: "github.com", Token: "account-a", APIHost: server.URL, Home: t.TempDir()}
	client, _ := NewClient(config)
	ctx := context.Background()
	key := Key{client.Repository("acme/repo"), 1}
	if _, err := client.Load(ctx, key.Repository.Path, 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	_, path, _ := client.observationLocation(key)
	var record prRecord
	if _, err := repostate.Read(path, &record); err != nil {
		t.Fatal(err)
	}
	record.ScopeIDs = []string{"discovered-by-watcher"}
	if err := repostate.Write(path, record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	config.Token = "account-b"
	other, _ := NewClient(config)
	if _, err := other.Load(ctx, key.Repository.Path, 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	title = "edited"
	if _, err := client.Update(ctx, key, UpdateRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if got, err := client.Load(ctx, key.Repository.Path, 1, time.Hour); err != nil || got.Title != "edited" || got.Key != key {
		t.Fatal(got, err)
	}
	if _, err := client.Close(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got, err := client.Load(ctx, key.Repository.Path, 1, time.Hour); err != nil || got.State != "closed" {
		t.Fatal(got, err)
	}
	if _, err := client.Merge(ctx, key, "abc", "merge"); err != nil {
		t.Fatal(err)
	}
	if got, err := client.Load(ctx, key.Repository.Path, 1, time.Hour); err != nil || got.State != "merged" {
		t.Fatal(got, err)
	}
	if _, err := repostate.Read(path, &record); err != nil || len(record.ScopeIDs) != 1 {
		t.Fatal(record, err)
	}
	if got, err := other.Load(ctx, key.Repository.Path, 1, time.Hour); err != nil || got.State != "open" {
		t.Fatal("other connection overwritten", got, err)
	}
}
func TestLifecycleOnlySnapshotRefreshesMissingMetadata(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, githubFixture(1, "open")) }))
	defer server.Close()
	client, _ := NewClient(Config{Host: "github.com", Token: "fixture", APIHost: server.URL, Home: t.TempDir()})
	key := Key{client.Repository("acme/repo"), 1}
	_, path, connection := client.observationLocation(key)
	snapshot := map[string]any{"repository": key.Repository, "number": 1, "state": "open", "title": "partial"}
	record := map[string]any{"version": 1, "connection": connection, "scopeIds": []string{"watcher"}, "failures": 0, "observation": map[string]any{"key": key, "snapshot": snapshot, "observedAt": time.Now().UTC(), "attemptedAt": time.Now().UTC()}}
	if err := repostate.Write(path, record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := client.Load(context.Background(), key.Repository.Path, 1, time.Hour)
	if err != nil || got.Key != key || got.Title != "fixture" || calls != 1 {
		t.Fatal(got, err, calls)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), connection+".json")); err != nil {
		t.Fatal(err)
	}
}
