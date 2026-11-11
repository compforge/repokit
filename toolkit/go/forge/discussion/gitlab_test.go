package discussion_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

// +case=Missing GitLab capability and resolution stay unknown; explicit resolve requires authoritative confirmation without retries
func TestGitLabDiscussionUnknownCapability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		notes          string
		individual     bool
		capability     *bool
		resolution     string
		responseNotes  string
		responseStatus int
		wantWrites     int
		errorCode      string
		uncertain      bool
	}{
		{name: "missing both", notes: `[{"id":1,"body":"finding"}]`, resolution: "unknown", responseNotes: `[{"id":1,"body":"finding","resolved":true}]`, wantWrites: 1},
		{name: "missing capability with known state", notes: `[{"id":1,"body":"finding","resolved":false}]`, resolution: "unresolved", responseNotes: `[{"id":1,"body":"finding","resolved":true}]`, wantWrites: 1},
		{name: "known capability missing state", notes: `[{"id":1,"body":"finding","resolvable":true}]`, capability: new(true), resolution: "unknown", responseNotes: `[{"id":1,"body":"finding","resolvable":true,"resolved":true}]`, wantWrites: 1},
		{name: "explicitly unsupported", notes: `[{"id":1,"body":"finding","resolvable":false}]`, capability: new(false), resolution: "not_applicable", errorCode: "forge.unsupported_operation"},
		{name: "individual note", notes: `[{"id":1,"body":"comment"}]`, individual: true, capability: new(false), resolution: "not_applicable", errorCode: "forge.unsupported_operation"},
		{name: "already resolved without capability", notes: `[{"id":1,"body":"finding","resolved":true}]`, resolution: "resolved"},
		{name: "unknown acknowledgement", notes: `[{"id":1,"body":"finding"}]`, resolution: "unknown", responseNotes: `[{"id":1,"body":"finding"}]`, wantWrites: 1, errorCode: "forge.outcome_unknown", uncertain: true},
		{name: "unresolved acknowledgement", notes: `[{"id":1,"body":"finding"}]`, resolution: "unknown", responseNotes: `[{"id":1,"body":"finding","resolved":false}]`, wantWrites: 1, errorCode: "forge.outcome_unknown", uncertain: true},
		{name: "authoritative rejection", notes: `[{"id":1,"body":"finding"}]`, resolution: "unknown", responseStatus: 403, wantWrites: 1, errorCode: "forge.access_denied"},
		{name: "one unresolved note dominates unknown", notes: `[{"id":1,"body":"finding","resolved":false},{"id":2,"body":"reply"}]`, resolution: "unresolved", responseNotes: `[{"id":1,"body":"finding","resolved":true},{"id":2,"body":"reply","resolved":true}]`, wantWrites: 1},
		{name: "unknown reply prevents resolved claim", notes: `[{"id":1,"body":"finding","resolvable":true,"resolved":true},{"id":2,"body":"reply"}]`, capability: new(true), resolution: "unknown", responseNotes: `[{"id":1,"body":"finding","resolved":true},{"id":2,"body":"reply"}]`, wantWrites: 1, errorCode: "forge.outcome_unknown", uncertain: true},
		{name: "irrelevant notes do not obscure state", notes: `[{"id":1,"body":"finding","resolvable":true,"resolved":true},{"id":2,"body":"plain","resolvable":false},{"id":3,"body":"system","system":true}]`, capability: new(true), resolution: "resolved"},
		{name: "explicit false does not erase missing capability", notes: `[{"id":1,"body":"plain","resolvable":false},{"id":2,"body":"reply"}]`, resolution: "unknown", responseNotes: `[{"id":1,"body":"plain","resolvable":false},{"id":2,"body":"reply","resolved":true}]`, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			wire := func(notes string) string {
				return fmt.Sprintf(`{"id":"thread","individual_note":%t,"notes":%s}`, tc.individual, notes)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if r.URL.EscapedPath() != "/projects/acme%2Frepo/merge_requests/7/discussions" {
						t.Error(r.URL)
					}
					fmt.Fprintf(w, "[%s]", wire(tc.notes))
					return
				}
				writes++
				if r.Method != http.MethodPut || r.URL.EscapedPath() != "/projects/acme%2Frepo/merge_requests/7/discussions/thread" {
					t.Error(r.Method, r.URL)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["resolved"] != true {
					t.Error(payload, err)
				}
				if tc.responseStatus != 0 {
					w.WriteHeader(tc.responseStatus)
					return
				}
				fmt.Fprint(w, wire(tc.responseNotes))
			}))
			defer server.Close()
			client, err := forge.NewClient(forge.Config{Host: "gitlab.com", APIHost: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			key := model.Key{Repository: client.Repository("acme/repo"), Number: 7}
			service := client.Discussions()
			rows, err := service.List(context.Background(), key)
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			row := rows[0]
			if (row.Resolvable == nil) != (tc.capability == nil) || row.Resolvable != nil && *row.Resolvable != *tc.capability || row.Resolution != tc.resolution {
				t.Fatalf("capability=%v resolution=%s", row.Resolvable, row.Resolution)
			}
			result, err := service.Resolve(context.Background(), key, "thread")
			if tc.errorCode == "" {
				if err != nil || result.Resolution != "resolved" {
					t.Fatal(result, err)
				}
			} else {
				var problem *errs.Error
				if !errors.As(err, &problem) || problem.Code != tc.errorCode || problem.Uncertain != tc.uncertain {
					t.Fatalf("unexpected resolve error: %v", err)
				}
			}
			if writes != tc.wantWrites {
				t.Fatalf("writes=%d want=%d", writes, tc.wantWrites)
			}
		})
	}
}
