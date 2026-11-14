package rebase

import (
	"errors"
	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
)

func (o *operation) finish() error {
	if e := o.requireRecord(); e != nil {
		return e
	}
	if o.report.Phase != "ready" && o.report.Phase != "publishing" && o.report.Phase != "published" {
		return failure("not_ready", "rebase must be complete before publication", nil)
	}
	if e := o.current(); e != nil {
		return e
	}
	if e := o.clean(); e != nil {
		return e
	}
	if o.record.PublishHead != "" && o.report.Head != o.record.PublishHead {
		return failure("identity_mismatch", "HEAD differs from the frozen publication commit", nil)
	}
	destination, e := o.destination(o.record.Remote)
	if e != nil {
		return e
	}
	if destination != o.record.Destination {
		return failure("destination_changed", "remote push destination changed since rebase start", nil)
	}
	contains, e := o.ancestor(o.record.TargetCommit, o.report.Head)
	if e != nil {
		return e
	}
	if !contains {
		return failure("identity_mismatch", "HEAD does not contain the frozen rebase target", nil)
	}
	var tip string
	e = o.measure("remote_preflight", func() error { var e error; tip, e = o.remoteTip(); return e })
	if e != nil {
		if o.record.Phase == "publishing" {
			return unknown("cannot reconcile prior publication; frozen commit and lease retained", e)
		}
		return e
	}
	desired := o.record.PublishHead
	if desired == "" {
		desired = o.report.Head
	}
	// +why=Observe equality before comparing the old lease: a lost successful push response is recoverable without replaying the write.
	if tip == desired {
		o.record.PublishHead = desired
		o.report.Confirmed = true
		o.publishState("published")
		return nil
	}
	if o.record.Phase == "published" || tip != o.record.ExpectedRemote {
		return failure("lease_conflict", "remote source moved since rebase start; no history was overwritten", nil)
	}
	if e = o.ctx.Err(); e != nil {
		return e
	}
	head, e := o.query("rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return e
	}
	if head != desired {
		return failure("identity_mismatch", "HEAD moved during publication preflight", nil)
	}
	o.record.PublishHead = desired
	o.record.Phase = "publishing"
	if e = writeRecord(o.info.GitDir, o.record); e != nil {
		return e
	}
	o.report.Phase = "publishing"
	var result command.Result
	ref := "refs/heads/" + o.record.Branch
	_ = o.measure("push", func() error {
		o.report.PushAttempted = true
		o.report.WriteAttempted = true
		result = o.run(o.ctx, o.repo, o.budget(), "push", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--force-with-lease="+ref+":"+o.record.ExpectedRemote, "--", o.record.Destination, desired+":"+ref)
		return result.Err
	})
	e = o.measure("remote_verify", func() error { var e error; tip, e = o.remoteTip(); return e })
	if e == nil && tip == desired {
		o.report.Confirmed = true
		o.publishState("published")
		return nil
	}
	if e == nil && !result.Uncertain && repogit.PushRejected(result.Stdout, ref) {
		return failure("push_rejected", "remote rejected the lease-protected publication; recovery record retained", result.Err)
	}
	return unknown("publication could not be confirmed; frozen commit and lease retained", errors.Join(result.Err, e))
}
