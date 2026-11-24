package process

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// PushDestination resolves exactly one configured push URL. Transport URLs stay private.
func PushDestination(repo, remote string, query func(time.Duration, ...string) (string, error)) (string, error) {
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
