package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/code/callers"
)

type CallersRequest = callers.Request
type CallersReport = callers.Report
type CallerSite = callers.Site

// FindCallers returns direct static call sites for the declaration at a one-based line and byte column.
func FindCallers(ctx context.Context, req CallersRequest) (CallersReport, error) {
	report, err := callers.Analyze(ctx, req)
	return report, errs.Wrap(err, "code.callers_failed", "code.callers", req.Path)
}
