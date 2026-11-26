package impact

import (
	"cmp"
	"context"
	"path"
	"slices"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/makefile"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// TestSelection records direct test evidence separately from transitive impact.
type TestSelection struct {
	Files            []string    `json:"files"`
	Reasons          []TestMatch `json:"reasons"`
	UnmatchedSources []string    `json:"unmatchedSources"`
}

type TestMatch struct {
	TestFile string              `json:"testFile"`
	Source   string              `json:"source"`
	Version  string              `json:"version"`
	Kind     string              `json:"kind"`
	Relation *codegraph.Relation `json:"relation,omitempty"`
}

func emptySelection() TestSelection {
	return TestSelection{Files: []string{}, Reasons: []TestMatch{}, UnmatchedSources: []string{}}
}

// +spec=Test recommendations use changed tests and direct use of changed declarations in a discovered test.
// +why=A shortest impact path can be a module import even when a separate direct call exists.
// +link=repo://docs/diff.md#test-discovery
func recommendTests(ctx context.Context, req Request, changes []FileChange, graphs []*codegraph.Graph, tests []string) TestSelection {
	selection := emptySelection()
	candidates := make(map[string]bool, len(tests))
	for _, test := range tests {
		candidates[test] = true
	}
	declared := declaredTests(ctx, req, candidates)
	for _, change := range changes {
		if IsTest(change.Path) {
			if candidates[change.Path] && change.Status != "deleted" {
				selection.Reasons = append(selection.Reasons, TestMatch{TestFile: change.Path, Source: change.Path, Version: "after", Kind: "changed_test"})
			}
			continue
		}
		if req.Gitlinks[change.Path] || !slices.Contains(project.FileTags(cg.Document{Path: change.Path}), cg.SourceTag) {
			continue
		}
		start := len(selection.Reasons)
		old := change.OldPath
		if old == "" {
			old = change.Path
		}
		for _, source := range unique([]string{old, change.Path}) {
			for _, test := range declared[source] {
				selection.Reasons = append(selection.Reasons, TestMatch{TestFile: test, Source: source, Version: "after", Kind: "make_prerequisite"})
			}
		}
		selection.Reasons = append(selection.Reasons, directMatches(graphs[0], old, "before", change.Before, candidates)...)
		selection.Reasons = append(selection.Reasons, directMatches(graphs[1], change.Path, "after", change.After, candidates)...)

		if len(selection.Reasons) == start {
			selection.UnmatchedSources = append(selection.UnmatchedSources, change.Path)
		}
	}
	for _, match := range selection.Reasons {
		selection.Files = append(selection.Files, match.TestFile)
	}
	selection.Files = unique(selection.Files)
	selection.UnmatchedSources = unique(selection.UnmatchedSources)
	slices.SortStableFunc(selection.Reasons, func(a, b TestMatch) int {
		return cmp.Or(cmp.Compare(a.TestFile, b.TestFile), cmp.Compare(a.Source, b.Source), cmp.Compare(a.Version, b.Version), cmp.Compare(a.Kind, b.Kind), cmp.Compare(matchLine(a), matchLine(b)))
	})
	return selection
}

func directMatches(graph *codegraph.Graph, source, version string, symbols []codegraph.Symbol, candidates map[string]bool) []TestMatch {
	var matches []TestMatch
	for _, symbol := range symbols {
		id := codegraph.SymbolID(source, symbol.QualifiedName)
		for _, edge := range graph.Incoming(id) {
			if candidates[edge.File] && codegraph.EvidenceRank(edge.Confidence) <= 1 && directTestUse(edge.Kind, symbol.Kind) {
				matches = append(matches, TestMatch{TestFile: edge.File, Source: source, Version: version, Kind: string(edge.Kind), Relation: &edge})
			}
		}
	}
	return matches
}

func matchLine(match TestMatch) int {
	if match.Relation == nil {
		return 0
	}
	return match.Relation.Line
}

// A value reference to a function can register it as a callback. Require a call
// site for a callable, and accept reads/writes for data declarations.
func directTestUse(relation codegraph.Kind, declaration string) bool {
	switch declaration {
	case "class", "interface", "struct", "namespace", "module":
		return false
	case "function", "method", "constructor":
		return relation == codegraph.Calls
	default:
		return relation == codegraph.Calls || relation == codegraph.Reads || relation == codegraph.Writes
	}
}

// A Component can declare an exact source-to-test dependency using ordinary
// Make syntax. Only existing test candidates from this checkout are runnable.
func declaredTests(ctx context.Context, req Request, candidates map[string]bool) map[string][]string {
	bySource := map[string][]string{}
	owners := map[string]bool{}
	for test := range candidates {
		if owner := req.NewLayout.Owner(test); owner != nil {
			owners[owner.Root] = true
		}
	}
	for root := range owners {
		contract := makefile.Parse(ctx, req.After[path.Join(root, "Makefile")])
		for target, sources := range contract.Prerequisites {
			test := path.Join(root, target)
			owner := req.NewLayout.Owner(test)
			if !candidates[test] || path.IsAbs(target) || owner == nil || owner.Root != root {
				continue
			}
			for _, source := range sources {
				if !path.IsAbs(source) {
					name := path.Join(root, source)
					bySource[name] = append(bySource[name], test)
				}
			}
		}
	}
	return bySource
}
