package git

import (
	"context"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// Query observes Git facts with a bounded subprocess budget.
type Query func(time.Duration, ...string) (string, error)

// BranchTip rejects unexpected, duplicate or malformed ref observations; an empty response is absence.
func BranchTip(output, ref string) (string, error) {
	if output == "" {
		return "", nil
	}
	fields := strings.Split(strings.TrimSuffix(output, "\n"), "\t")
	if len(fields) != 2 || fields[1] != ref || !ValidObjectID(fields[0]) {
		return "", &errs.Error{Code: "git.invalid_response", Message: "invalid remote branch observation"}
	}
	return fields[0], nil
}

// PushBudget reserves time for remote reconciliation without extending the caller deadline.
func PushBudget(ctx context.Context) time.Duration {
	budget := 2 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		budget = min(budget, remaining-min(30*time.Second, remaining/5))
	}
	return budget
}
