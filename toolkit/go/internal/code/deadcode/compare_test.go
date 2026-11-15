package deadcode

import (
	"context"
	"slices"
	"testing"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

// +case:id=deadcode_diff_identity_ambiguity,desc=`Either version has multiple declarations with the same identity`,expect=`Every affected candidate remains unmatched with ambiguous_identity and no guessed previous declaration`
func TestDeadcodeDiffAmbiguousIdentity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []string
	}{
		{"before", []string{"old-1", "old-2"}, []string{"new"}},
		{"after", []string{"old"}, []string{"new-1", "new-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := inspectComparisonFixture(t, comparisonFixture(tc.before...))
			after := inspectComparisonFixture(t, comparisonFixture(tc.after...))
			got, err := compare(context.Background(), before, after, []string{"changed.go"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, candidate := range got {
				ids = append(ids, candidate.ID)
				if candidate.Change != "unmatched" || !slices.Contains(candidate.Risks, "ambiguous_identity") || candidate.Previous != nil {
					t.Fatalf("ambiguous candidate=%+v", candidate)
				}
			}
			if !slices.Equal(ids, tc.after) {
				t.Fatalf("candidates=%v want=%v", ids, tc.after)
			}
		})
	}
}

// +case:id=deadcode_diff_baseline_gaps,desc=`Capture or graph diagnostics leave gaps in a missing or matched baseline declaration`,expect=`Missing identities stay unmatched; matched candidates preserve their change state and baseline_gaps risk`
func TestDeadcodeDiffBaselineGaps(t *testing.T) {
	for _, baseline := range []struct {
		name, change  string
		present, used bool
	}{
		{"missing", "introduced", false, false},
		{"referenced", "became_dead", true, true},
		{"unreferenced", "pre_existing", true, false},
	} {
		for _, coverage := range []struct {
			name        string
			complete    bool
			diagnostics []shared.Diagnostic
			gap         bool
		}{
			{name: "complete", complete: true},
			{name: "capture_gap", complete: false, gap: true},
			{name: "graph_gap", complete: true, diagnostics: []shared.Diagnostic{{Code: "parse_error"}}, gap: true},
		} {
			t.Run(baseline.name+"/"+coverage.name, func(t *testing.T) {
				graph := comparisonFixture()
				if baseline.present {
					graph = comparisonFixture("old")
				}
				if baseline.used {
					graph.Relations = append(graph.Relations, shared.Relation{Source: "doc", Target: "old", Kind: shared.Calls})
				}
				graph.Snapshot.Complete = coverage.complete
				graph.Diagnostics = coverage.diagnostics
				before := inspectComparisonFixture(t, graph)
				after := inspectComparisonFixture(t, comparisonFixture("new"))
				got, err := compare(context.Background(), before, after, []string{"changed.go"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				want := baseline.change
				if !baseline.present && coverage.gap {
					want = "unmatched"
				}
				if len(got) != 1 || got[0].ID != "new" || got[0].Change != want || slices.Contains(got[0].Risks, "baseline_gaps") != coverage.gap {
					t.Fatalf("candidates=%+v want change=%s baseline_gaps=%t", got, want, coverage.gap)
				}
				previous := got[0].Previous
				if baseline.present {
					if previous == nil || previous.ID != "old" {
						t.Fatalf("lost matched declaration: %+v", got[0])
					}
				} else if previous != nil {
					t.Fatalf("invented previous declaration: %+v", got[0])
				}
			})
		}
	}
}

func comparisonFixture(ids ...string) *analysis.GraphSnapshot {
	graph := &analysis.GraphSnapshot{
		Snapshot: analysis.SnapshotReport{Complete: true},
		Nodes:    []shared.Node{{ID: "doc", Kind: shared.DocumentNodeKind, Path: "changed.go"}},
	}
	for _, id := range ids {
		graph.Nodes = append(graph.Nodes, shared.Node{ID: id, Kind: shared.Function, Name: "f", QualifiedName: "p.f", Language: "go", Location: &shared.Location{Path: "changed.go"}})
		graph.Relations = append(graph.Relations, shared.Relation{Source: "doc", Target: id, Kind: shared.Declares})
	}
	return graph
}

func inspectComparisonFixture(t *testing.T, graph *analysis.GraphSnapshot) *graphIndex {
	t.Helper()
	result, err := inspect(context.Background(), graph, Request{Mode: "unreferenced"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
