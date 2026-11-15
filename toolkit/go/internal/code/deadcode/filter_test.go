package deadcode

import (
	"testing"

	shared "github.com/compforge/codegraph"
)

// +spec=Entrypoint report policy consumes CodeGraph facts independently of language and naming conventions.
func TestDeadcodeConsumesEntrypointFacts(t *testing.T) {
	nodes := []Candidate{
		{Node: shared.Node{ID: "native", Language: "future-language", Name: "start", Kind: shared.Function, Entrypoint: true}},
		{Node: shared.Node{ID: "ordinary-main", Language: "go", Name: "main", Kind: shared.Function}},
		{Node: shared.Node{ID: "ordinary-init", Language: "go", Name: "init", Kind: shared.Function}},
	}
	got := filterCandidates(nodes, Request{ExcludeEntrypoints: true})
	if len(got) != 2 || got[0].ID != "ordinary-main" || got[1].ID != "ordinary-init" {
		t.Fatalf("report policy did not follow native facts: %+v", got)
	}
}
