package git

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// Query observes Git facts with a bounded subprocess budget.
type Query func(time.Duration, ...string) (string, error)

// PushDestination resolves exactly one configured push URL. Transport URLs stay private.
func PushDestination(repo, remote string, query Query) (string, error) {
	names, e := query(5*time.Second, "remote")
	if e != nil {
		return "", e
	}
	if remote == "" || strings.HasPrefix(remote, "-") || !slices.Contains(strings.Split(names, "\n"), remote) {
		return "", &errs.Error{Code: "git.invalid_argument", Message: "remote must name a configured Git remote"}
	}
	endpoint, e := query(5*time.Second, "remote", "get-url", "--push", "--all", remote)
	if e != nil {
		return "", e
	}
	if endpoint == "" || strings.ContainsAny(endpoint, "\r\n") {
		return "", &errs.Error{Code: "git.ambiguous_remote", Message: "publication requires exactly one push destination"}
	}
	if !strings.Contains(endpoint, ":") && !filepath.IsAbs(endpoint) {
		endpoint, e = filepath.Abs(filepath.Join(repo, endpoint))
	}
	return endpoint, e
}

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

// PushRejected recognizes Git's porcelain rejection, never localized stderr.
func PushRejected(output, ref string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || fields[0] != "!" {
			continue
		}
		_, dest, ok := strings.Cut(fields[1], ":")
		if ok && dest == ref && (strings.HasPrefix(fields[2], "[rejected]") || strings.HasPrefix(fields[2], "[remote rejected]")) {
			return true
		}
	}
	return false
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
