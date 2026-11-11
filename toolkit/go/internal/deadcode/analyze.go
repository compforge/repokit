package deadcode

import (
	"context"
	"fmt"
	"slices"

	"github.com/compforge/repokit/toolkit/go/internal/impact"
)

// Analyze applies the same rule to both versions before selecting candidates.
// +spec=`Candidate filters never remove callers from either graph or the baseline comparison`
func Analyze(ctx context.Context, after Graph, comparison *Comparison, opts Options) (Result, error) {
	current, err := inspect(ctx, &after, opts)
	if err != nil {
		return Result{}, err
	}
	result := Result{Candidates: current.candidates, Roots: current.roots, BeforeRoots: []Root{}}
	if comparison != nil {
		previous, err := inspect(ctx, &comparison.Before, opts)
		if err != nil {
			return Result{}, fmt.Errorf("before analysis: %w", err)
		}
		result.BeforeRoots = previous.roots
		result.Candidates, err = compare(ctx, previous, current, comparison.ChangedFiles, comparison.Renames)
		if err != nil {
			return Result{}, err
		}
	}
	result.Candidates = filterCandidates(result.Candidates, opts)
	result.Candidates = slices.DeleteFunc(result.Candidates, func(candidate Candidate) bool {
		return opts.Files != nil && (candidate.Location == nil || !slices.Contains(opts.Files, candidate.Location.Path))
	})
	if comparison != nil && !comparison.Complete {
		for i := range result.Candidates {
			result.Candidates[i].Risks = append(result.Candidates[i].Risks, "incomplete_comparison")
		}
	}
	return result, nil
}

// +spec=Candidate filters preserve callers from tests and runtime entrypoints in the full graph.
func filterCandidates(nodes []Candidate, req Options) []Candidate {
	if !req.ExcludeTests && !req.ExcludeEntrypoints {
		return nodes
	}
	return slices.DeleteFunc(nodes, func(node Candidate) bool {
		if req.ExcludeTests && node.Location != nil && impact.IsTest(node.Location.Path) {
			return true
		}
		// CodeGraph owns language rules; report policy only consumes its facts.
		return req.ExcludeEntrypoints && node.Entrypoint
	})
}
