package branch

import (
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

func (o *operation) create(opts Options) error {
	if opts.Base == "" {
		return failure("invalid_argument", "explicit base is required", nil)
	}
	if o.record != nil && !terminal(o.record) {
		return failure("recovery_required", "unfinished branch creation; inspect recoveryPath and stashOid before any further write", nil)
	}
	if e := o.idle(); e != nil {
		return e
	}
	if len(o.report.Conflicts) > 0 {
		return failure("busy", "index contains unresolved conflicts", nil)
	}
	if o.report.CurrentBranch == opts.Name {
		o.report.Action = "reused"
		o.report.Confirmed = true
		o.report.BranchConfirmed = true
		o.report.ChangesRestored = true
		return nil
	}
	if _, exists, e := gitworktree.BranchCommit(o.ctx, o.repo, opts.Name); e != nil {
		return e
	} else if exists {
		return failure("exists", "branch already exists; select it explicitly instead of recreating it", nil)
	}
	dirty, e := o.dirty()
	if e != nil {
		return e
	}
	if dirty && !opts.CarryChanges {
		return failure("dirty", "working tree is dirty; pass --carry-changes to preserve these changes on the new branch", nil)
	}
	var base string
	e = o.measure("resolve_base", func() error {
		var err error
		if opts.Remote != "" {
			base, err = gitworktree.RefreshBranch(o.ctx, o.repo, opts.Remote, opts.Base)
		} else {
			base, err = gitworktree.ResolveCommit(o.ctx, o.repo, opts.Base)
		}
		return err
	})
	if e != nil {
		return failure("base_unavailable", "cannot resolve a fresh branch base", e)
	}
	marker, e := newMarker()
	if e != nil {
		return e
	}
	o.record = &record{Version: 1, Branch: opts.Name, BaseCommit: base, SourceBranch: o.report.CurrentBranch, SourceHead: o.report.Head, Marker: marker, HadChanges: dirty, ChangesRestored: !dirty}
	o.report.BaseCommit = base
	o.report.ChangesRestored = !dirty
	if e = o.save("prepared"); e != nil {
		return e
	}
	if e = o.sourceUnchanged(); e != nil {
		return e
	}
	if dirty {
		if e = o.measure("save_changes", o.saveChanges); e != nil {
			return e
		}
	}
	// Stashing cannot remove dirty nested repositories. Never switch while any changes remain.
	if remaining, err := o.dirty(); err != nil {
		return err
	} else if remaining {
		return failure("dirty", "checkout still has local changes; branch was not created; inspect the retained stash before recovery", nil)
	}
	if e = o.save("creating"); e != nil {
		return e
	}
	o.report.WriteAttempted = true
	var checkout gitworktree.GitResult
	_ = o.measure("create_branch", func() error {
		checkout = o.run(o.ctx, o.repo, 2*time.Minute, "checkout", "--no-overwrite-ignore", "-b", opts.Name, base, "--")
		return nil
	})
	if checkout.Uncertain {
		return unknown("branch checkout result is unknown", checkout.Err)
	}
	if checkout.Err != nil {
		if e = o.sourceUnchanged(); e != nil {
			return unknown("failed checkout changed its source identity", e)
		}
		if dirty {
			remaining, err := o.dirty()
			if err != nil || remaining {
				return unknown("failed checkout left changes; retained stash must be inspected", err)
			}
			if e = o.measure("restore_changes", o.restore); e != nil {
				return e
			}
		}
		o.complete("failed")
		return failure("checkout_failed", "branch creation failed; source checkout retained and any saved changes restored", checkout.Err)
	}
	b, h, e := o.identity()
	if e != nil {
		return unknown("cannot confirm created branch", e)
	}
	o.report.CurrentBranch = b
	o.report.Head = h
	if b != opts.Name || h != base {
		return unknown("created branch does not match the frozen base", nil)
	}
	o.report.Action = "created"
	o.report.BranchConfirmed = true
	if dirty {
		if e = o.save("created"); e != nil {
			return unknown("branch created but recovery record publication failed; changes remain in stash", e)
		}
		if e = o.measure("restore_changes", o.restore); e != nil {
			return e
		}
	} else {
		o.complete("created")
	}
	o.report.Confirmed = true
	return nil
}

func (o *operation) saveChanges() error {
	if e := o.save("saving"); e != nil {
		return e
	}
	o.report.WriteAttempted = true
	r := o.run(o.ctx, o.repo, 2*time.Minute, "stash", "push", "--include-untracked", "--message", o.record.Marker)
	// Recover only the identity of this operation's stash, never a positional stack entry.
	stash, e := o.findStash()
	if e == nil {
		o.record.StashOID = stash
		o.report.StashOID = stash
	}
	if r.Uncertain {
		return unknown("saving local changes has an unknown result", r.Err)
	}
	if r.Err != nil {
		return failure("stash_failed", "saving local changes failed; inspect recovery evidence before retrying", r.Err)
	}
	if e != nil || stash == "" {
		return unknown("cannot identify the saved changes", e)
	}
	return o.save("saved")
}

func (o *operation) restore() error {
	if e := o.save("restoring"); e != nil {
		return e
	}
	r := o.run(o.ctx, o.repo, 2*time.Minute, "stash", "apply", "--index", o.record.StashOID)
	if r.Uncertain {
		return unknown("restoring local changes has an unknown result; backup retained", r.Err)
	}
	if r.Err != nil {
		paths, e := o.query("diff", "--name-only", "--diff-filter=U", "-z")
		if e == nil && paths != "" {
			o.report.Conflicts = strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00")
		}
		o.complete("conflict")
		return failure("restore_failed", "local changes were not fully restored; inspect index and working tree; backup stash retained", r.Err)
	}
	o.record.ChangesRestored = true
	o.report.ChangesRestored = true
	o.complete("restored")
	return nil
}
