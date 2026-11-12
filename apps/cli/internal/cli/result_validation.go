package cli

import (
	"github.com/compforge/repokit/apps/cli/internal/cli/validation"
	"github.com/compforge/repokit/toolkit/go"
)

func describeValidation(r *commandResult, report repokit.ValidationReport) {
	view := validation.Describe(report)
	r.Summary = view.Summary
	if view.Outcome != "" {
		r.Outcome = view.Outcome
	}
	r.Diagnostics = append(r.Diagnostics, view.Diagnostics...)
	r.Cost.Validation = &view.Cost
}
