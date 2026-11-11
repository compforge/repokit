package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/analysis/deadcode"
)

// DeadcodeRequest selects the snapshot and optional node/relation budgets.
type DeadcodeRequest = deadcode.Request

// DeadcodeCandidate preserves a declaration and the reason, risks, and before-use evidence.
type DeadcodeCandidate = deadcode.Candidate
type DeadcodeUse = deadcode.Use
type DeadcodeRoot = deadcode.Root
type DeadcodeScope = deadcode.Scope
type DeadcodeBaseline = deadcode.Baseline

// DeadcodeReport separates candidate scope from whole-snapshot graph evidence.
type DeadcodeReport = deadcode.Report

// AnalyzeDeadcode analyzes a full captured graph. Base selects a before/after
// comparison seeded by changed documents; Files only filters returned candidates.
// Unreachable mode uses declared roots and reports test-only reachability separately.
// Candidates are observations with risks, never safe-deletion verdicts.
func AnalyzeDeadcode(ctx context.Context, req DeadcodeRequest) (DeadcodeReport, error) {
	result, err := deadcode.Analyze(ctx, req)
	return result, errs.Wrap(err, "deadcode.failed", "deadcode", req.Repository)
}
