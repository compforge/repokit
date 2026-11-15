package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/code/dead"
)

// DeadcodeRequest selects the snapshot and optional node/relation budgets.
type DeadcodeRequest = dead.Request

// DeadcodeCandidate preserves a declaration and the reason, risks, and before-use evidence.
type DeadcodeCandidate = dead.Candidate
type DeadcodeUse = dead.Use
type DeadcodeRoot = dead.Root
type DeadcodeScope = dead.Scope
type DeadcodeBaseline = dead.Baseline

// DeadcodeReport separates candidate scope from whole-snapshot graph evidence.
type DeadcodeReport = dead.Report

// AnalyzeDeadcode analyzes a full captured graph. Base selects a before/after
// comparison seeded by changed documents; Files only filters returned candidates.
// Unreachable mode uses declared roots and reports test-only reachability separately.
// Candidates are observations with risks, never safe-deletion verdicts.
func AnalyzeDeadcode(ctx context.Context, req DeadcodeRequest) (DeadcodeReport, error) {
	result, err := dead.Analyze(ctx, req)
	return result, errs.Wrap(err, "deadcode.failed", "deadcode", req.Repository)
}
