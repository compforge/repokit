package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

// DeadcodeRequest selects the snapshot and optional node/relation budgets.
type DeadcodeRequest = analysis.DeadcodeRequest

// DeadcodeCandidate preserves a declaration and the reason, risks, and before-use evidence.
type DeadcodeCandidate = analysis.DeadcodeCandidate
type DeadcodeUse = analysis.DeadcodeUse
type DeadcodeRoot = analysis.DeadcodeRoot
type DeadcodeScope = analysis.DeadcodeScope
type DeadcodeBaseline = analysis.DeadcodeBaseline

// DeadcodeReport separates candidate scope from whole-snapshot graph evidence.
type DeadcodeReport = analysis.DeadcodeReport

// AnalyzeDeadcode analyzes a full captured graph. Base selects a before/after
// comparison seeded by changed documents; Files only filters returned candidates.
// Unreachable mode uses declared roots and reports test-only reachability separately.
// Candidates are observations with risks, never safe-deletion verdicts.
func AnalyzeDeadcode(ctx context.Context, req DeadcodeRequest) (DeadcodeReport, error) {
	result, err := analysis.AnalyzeDeadcode(ctx, req)
	return result, errs.Wrap(err, "deadcode.failed", "deadcode", req.Repository)
}
