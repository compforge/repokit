package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

// DeadcodeRequest selects the snapshot and optional node/relation budgets.
type DeadcodeRequest = analysis.DeadcodeRequest

// DeadcodeReport lists declaration nodes without incoming usage edges, alongside
// snapshot identity and graph diagnostics. Candidates are not deletion verdicts.
type DeadcodeReport = analysis.DeadcodeReport

// AnalyzeDeadcode builds a graph of all captured text documents and finds
// declarations without incoming non-structural edges. It retains all confidence
// levels and self-references. Optional filters omit test-file candidates and Go
// runtime entrypoints after graph construction; public APIs remain candidates.
func AnalyzeDeadcode(ctx context.Context, req DeadcodeRequest) (DeadcodeReport, error) {
	result, err := analysis.AnalyzeDeadcode(ctx, req)
	return result, errs.Wrap(err, "deadcode.failed", "deadcode", req.Repository)
}
