package validation

import (
	"encoding/json"
	"testing"

	"github.com/compforge/repokit/toolkit/go/internal/validation/history"
)

// +case=Evidence from an earlier input contract cannot authorize Component-scoped reuse.
func TestReuseRequiresMatchingInputContract(t *testing.T) {
	spec := CheckSpec{InputVersion: 2, Check: "test", Scope: "full"}
	manifest := inputManifest{Checkout: "repo", Files: map[string]inputEntry{}}
	s := historySession{
		manifest:  manifest,
		manifests: map[string]inputManifest{"inputs": manifest},
		state:     &history.State{Runs: map[string]*history.Run{}, Order: []string{"current", "old"}},
	}
	for id, version := range map[string]int{"old": 0, "current": 2} {
		check := CheckResult{CheckSpec: spec, Status: "passed"}
		check.InputVersion = version
		data, err := json.Marshal(check)
		if err != nil {
			t.Fatal(err)
		}
		s.state.Runs[id] = &history.Run{Start: history.Event{Checkout: "repo", Component: "component", Inputs: "inputs", Run: id}, Result: data, Check: "test", Status: "passed"}
	}
	run, _, _, err := s.candidate("component", spec)
	if err != nil || run != s.state.Runs["current"] {
		t.Fatalf("%+v %v", run, err)
	}
	s.state.Order = []string{"old"}
	run, _, reason, err := s.candidate("component", spec)
	if err != nil || run != nil || reason != "check input contract changed" {
		t.Fatalf("%+v %s %v", run, reason, err)
	}
}
