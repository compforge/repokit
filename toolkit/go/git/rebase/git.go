package rebase

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (o *operation) query(args ...string) (string, error) { return o.queryFor(5*time.Second, args...) }
func (o *operation) queryFor(timeout time.Duration, args ...string) (string, error) {
	r := o.run(o.ctx, o.repo, timeout, args...)
	if r.Err != nil {
		return "", failure("query_failed", "Git observation failed during rebase", r.Err)
	}
	return strings.TrimSuffix(r.Stdout, "\n"), nil
}
func (o *operation) branch() (string, error) {
	r := o.run(o.ctx, o.repo, 5*time.Second, "symbolic-ref", "--quiet", "HEAD")
	if r.ExitCode == 1 && !r.Uncertain {
		return "", nil
	}
	if r.Err != nil {
		return "", failure("query_failed", "cannot observe checkout branch", r.Err)
	}
	return strings.TrimPrefix(strings.TrimSuffix(r.Stdout, "\n"), "refs/heads/"), nil
}
func (o *operation) ancestor(base, head string) (bool, error) {
	r := o.run(o.ctx, o.repo, 5*time.Second, "merge-base", "--is-ancestor", base, head)
	if r.ExitCode == 1 && !r.Uncertain {
		return false, nil
	}
	return r.Err == nil, r.Err
}
func (o *operation) clean() error {
	status, e := o.query("status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil {
		return e
	}
	if status != "" {
		return failure("dirty", "checkout must be clean; preserve local changes before this action", nil)
	}
	return nil
}
func (o *operation) current() error {
	b, e := o.branch()
	if e != nil {
		return e
	}
	if b != o.record.Branch {
		return failure("identity_mismatch", "checkout is not on the transaction's source branch", nil)
	}
	return nil
}
func (o *operation) native() (string, error) {
	found := ""
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		path := filepath.Join(o.info.GitDir, name)
		fi, e := os.Stat(path)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return "", e
		}
		if !fi.IsDir() || found != "" {
			return "", failure("identity_mismatch", "ambiguous Git rebase metadata", nil)
		}
		found = path
	}
	return found, nil
}
func (o *operation) observe() error {
	head, e := o.query("rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return e
	}
	o.report.Head = head
	native, e := o.native()
	if e != nil {
		return e
	}
	if o.record == nil {
		o.report.Phase = "none"
		if native != "" {
			o.report.Phase = "unmanaged"
		}
		return nil
	}
	o.report.Transaction = &o.record.Transaction
	o.report.Phase = o.record.Phase
	if native != "" {
		if terminal(o.record.Phase) || o.record.Phase == "publishing" {
			return failure("identity_mismatch", "Git rebase does not belong to the saved transaction", nil)
		}
		for name, want := range map[string]string{"head-name": "refs/heads/" + o.record.Branch, "orig-head": o.record.StartedHead, "onto": o.record.TargetCommit} {
			raw, e := os.ReadFile(filepath.Join(native, name))
			if e != nil {
				return e
			}
			if strings.TrimSuffix(string(raw), "\n") != want {
				return failure("identity_mismatch", "Git rebase identity differs from the saved transaction", nil)
			}
		}
		paths, e := o.query("diff", "--name-only", "--diff-filter=U", "-z")
		if e != nil {
			return e
		}
		o.report.Conflicts = []string{}
		if paths != "" {
			o.report.Conflicts = strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00")
		}
		o.report.Phase = "paused"
		return nil
	}
	if terminal(o.record.Phase) {
		return nil
	}
	if e = o.current(); e != nil {
		return e
	}
	if o.record.Phase == "publishing" {
		if head != o.record.PublishHead {
			return failure("identity_mismatch", "HEAD changed after publication intent was frozen", nil)
		}
		return nil
	}
	contains, e := o.ancestor(o.record.TargetCommit, head)
	if e != nil {
		return e
	}
	if contains {
		o.report.Phase = "ready"
	} else {
		o.report.Phase = "unknown"
	}
	return nil
}
func (o *operation) destination(remote string) (string, error) {
	remotes, e := o.query("remote")
	if e != nil {
		return "", e
	}
	if remote == "" || strings.HasPrefix(remote, "-") || !slices.Contains(strings.Split(remotes, "\n"), remote) {
		return "", failure("invalid_argument", "remote must name a configured Git remote", nil)
	}
	endpoint, e := o.query("remote", "get-url", "--push", "--all", remote)
	if e != nil {
		return "", e
	}
	if endpoint == "" || strings.ContainsAny(endpoint, "\r\n") {
		return "", failure("ambiguous_remote", "rebase requires exactly one push destination", nil)
	}
	if !strings.Contains(endpoint, ":") && !filepath.IsAbs(endpoint) {
		endpoint = filepath.Join(o.repo, endpoint)
	}
	return endpoint, nil
}
func (o *operation) remoteTip() (string, error) {
	ref := "refs/heads/" + o.record.Branch
	raw, e := o.queryFor(30*time.Second, "ls-remote", "--refs", "--", o.record.Destination, ref)
	if e != nil {
		return "", e
	}
	for _, line := range strings.Split(raw, "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && name == ref && oid(sha) {
			return sha, nil
		}
	}
	if raw == "" {
		return "", nil
	}
	return "", failure("query_failed", "invalid remote branch observation", nil)
}
func (o *operation) budget() time.Duration {
	budget := 2 * time.Minute
	if deadline, ok := o.ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		budget = min(budget, remaining-min(30*time.Second, remaining/5))
	}
	return budget
}
