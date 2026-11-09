package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	config := Config{Host: "github.com", Token: "account-a", APIHost: server.URL, StateDir: t.TempDir()}
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
