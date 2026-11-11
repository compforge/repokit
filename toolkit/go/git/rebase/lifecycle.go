package rebase

import (
	"errors"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

func (o *operation) start(opts Options) error {
	if o.report.Phase == "unmanaged" || (o.record != nil && !terminal(o.record.Phase)) {
		return failure("active", "a rebase transaction already exists; inspect status before continuing", nil)
	}
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	for _, branch := range []string{opts.Branch, opts.Target} {
		if e := gitworktree.ValidateBranch(o.ctx, o.repo, branch); e != nil {
			return failure("invalid_argument", "explicit valid source and target branches are required", e)
		}
	}
	if opts.Branch == opts.Target {
		return failure("invalid_argument", "source and target branches must differ", nil)
	}
	current, e := o.branch()
	if e != nil {
		return e
	}
	if current != opts.Branch {
		return failure("identity_mismatch", "requested source branch is not checked out", nil)
	}
	if e = o.clean(); e != nil {
		return e
	}
	destination, e := o.destination(opts.Remote)
	if e != nil {
		return e
	}
	r := &record{Version: 1, Phase: "prepared", Destination: destination, Transaction: Transaction{Branch: opts.Branch, Target: opts.Target, Remote: opts.Remote, StartedHead: o.report.Head, StartedAt: time.Now().UTC()}}
	o.record = r
	o.report.Transaction = &r.Transaction
	e = o.measure("remote_prepare", func() error {
		var e error
		r.ExpectedRemote, e = o.remoteTip()
		if e != nil {
			return e
		}
		if r.ExpectedRemote == "" {
			return failure("missing_remote", "source branch does not exist at the push destination", nil)
		}
		// Fetch objects from the push destination without changing tracking refs or FETCH_HEAD.
		if _, e = o.queryFor(2*time.Minute, "fetch", "--no-tags", "--no-write-fetch-head", "--", r.Destination, "refs/heads/"+r.Branch); e != nil {
			return e
		}
		contains, e := o.ancestor(r.ExpectedRemote, r.StartedHead)
		if e != nil {
			return e
		}
		if !contains {
			return failure("diverged", "local source must contain the captured remote source commit", nil)
		}
		r.TargetCommit, e = gitworktree.RefreshBranch(o.ctx, o.repo, r.Remote, r.Target)
		if e != nil {
			return failure("fetch_failed", "could not refresh rebase target", e)
		}
		current, e := o.remoteTip()
		if e != nil {
			return e
		}
		if current != r.ExpectedRemote {
			return failure("lease_conflict", "remote source moved while preparing rebase; no rewrite started", nil)
		}
		return nil
	})
	if e != nil {
		return e
	}
	// Recheck before persisting intent: external Git is not governed by our cooperative lease.
	if e = o.current(); e != nil {
		return e
	}
	if e = o.clean(); e != nil {
		return e
	}
	head, e := o.query("rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return e
	}
	if head != r.StartedHead {
		return failure("identity_mismatch", "HEAD moved while preparing rebase", nil)
	}
	if e = writeRecord(o.info.GitDir, r); e != nil {
		return e
	}
	o.report.Phase = "prepared"
	return o.rewrite("rebase", "-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false", "rebase", "--no-autostash", "--no-update-refs", "--no-fork-point", "--no-rebase-merges", r.TargetCommit)
}
func (o *operation) requireRecord() error {
	if o.record == nil {
		return failure("missing", "no saved rebase transaction in this checkout", nil)
	}
	return nil
}
func (o *operation) continueRebase() error {
	if e := o.requireRecord(); e != nil {
		return e
	}
	if o.report.Phase == "ready" {
		o.report.Confirmed = true
		return nil
	}
	if o.report.Phase != "paused" {
		return failure("not_paused", "transaction has no matching paused Git rebase", nil)
	}
	return o.rewrite("continue", "-c", "core.editor=true", "-c", "rebase.autoStash=false", "rebase", "--continue")
}
func (o *operation) rewrite(phase string, args ...string) error {
	var result gitworktree.GitResult
	_ = o.measure(phase, func() error {
		o.report.WriteAttempted = true
		result = o.run(o.ctx, o.repo, 2*time.Minute, args...)
		return result.Err
	})
	if result.Uncertain {
		o.report.Phase = "unknown"
		return unknown("Git rewrite was interrupted; recovery record retained", result.Err)
	}
	e := o.measure("verify", o.observe)
	if e != nil {
		return unknown("could not verify Git rewrite; recovery record retained", errors.Join(result.Err, e))
	}
	if o.report.Phase == "paused" || (o.report.Phase == "ready" && result.Err == nil) {
		o.report.Confirmed = true
		o.publishState(o.report.Phase)
		return nil
	}
	// A failed hook or command is not a successful rewrite even if its target is an ancestor.
	if result.Err != nil {
		if o.report.Head == o.record.StartedHead {
			o.publishState("failed")
		}
		return failure("rewrite_failed", "Git rewrite failed; inspect the checkout and recovery record", result.Err)
	}
	return unknown("rewrite exited without a verifiable target; recovery record retained", nil)
}
func (o *operation) abort() error {
	if e := o.requireRecord(); e != nil {
		return e
	}
	if o.report.Phase == "aborted" {
		o.report.Confirmed = true
		return nil
	}
	if o.report.Phase == "unknown" && o.report.Head == o.record.StartedHead {
		o.report.Confirmed = true
		o.publishState("aborted")
		return nil
	}
	if o.report.Phase != "paused" {
		return failure("not_paused", "abort only restores an in-progress rebase; completed history is never reset", nil)
	}
	result := gitworktree.GitResult{}
	_ = o.measure("abort", func() error {
		o.report.WriteAttempted = true
		result = o.run(o.ctx, o.repo, 2*time.Minute, "rebase", "--abort")
		return result.Err
	})
	if result.Uncertain {
		return unknown("Git abort was interrupted; recovery record retained", result.Err)
	}
	if result.Err != nil {
		return failure("abort_failed", "Git abort failed; recovery record retained", result.Err)
	}
	e := o.measure("verify", func() error {
		if e := o.current(); e != nil {
			return e
		}
		n, e := o.native()
		if e != nil {
			return e
		}
		if n != "" {
			return failure("abort_failed", "Git rebase still exists after abort", nil)
		}
		head, e := o.query("rev-parse", "--verify", "HEAD^{commit}")
		o.report.Head = head
		if e != nil {
			return e
		}
		if head != o.record.StartedHead {
			return failure("identity_mismatch", "abort did not restore the original source commit", nil)
		}
		return nil
	})
	if e != nil {
		return unknown("could not confirm rebase abort", e)
	}
	o.report.Conflicts = []string{}
	o.report.Confirmed = true
	o.publishState("aborted")
	return nil
}
