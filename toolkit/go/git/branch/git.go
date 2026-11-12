package branch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (o *operation) query(args ...string) (string, error) {
	r := o.run(o.ctx, o.repo, 5*time.Second, args...)
	if r.Err != nil {
		return "", failure("query_failed", "Git observation failed during branch creation", r.Err)
	}
	return strings.TrimSuffix(r.Stdout, "\n"), nil
}
func (o *operation) identity() (string, string, error) {
	r := o.run(o.ctx, o.repo, 5*time.Second, "symbolic-ref", "--quiet", "HEAD")
	branch := ""
	if r.Err == nil {
		branch = strings.TrimPrefix(strings.TrimSuffix(r.Stdout, "\n"), "refs/heads/")
	} else if r.ExitCode != 1 || r.Uncertain {
		return "", "", r.Err
	}
	head, e := o.query("rev-parse", "--verify", "HEAD^{commit}")
	return branch, head, e
}
func (o *operation) dirty() (bool, error) {
	s, e := o.query("status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	return s != "", e
}
func (o *operation) idle() error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "index.lock"} {
		_, e := os.Lstat(filepath.Join(o.info.GitDir, name))
		if e == nil {
			return failure("busy", "checkout has an active Git operation", nil)
		}
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return nil
}
func (o *operation) findStash() (string, error) {
	raw, e := o.query("stash", "list", "--format=%H", "--fixed-strings", "--grep="+o.record.Marker, "--max-count=2")
	if e != nil {
		return "", e
	}
	if raw == "" {
		return "", nil
	}
	if !oid(raw) {
		return "", failure("stash_identity", "saved stash identity is ambiguous", nil)
	}
	return raw, nil
}
func (o *operation) observe() error {
	b, h, e := o.identity()
	if e != nil {
		return e
	}
	o.report.CurrentBranch = b
	o.report.Head = h
	o.report.BranchConfirmed = b == o.report.Branch
	paths, e := o.query("diff", "--name-only", "--diff-filter=U", "-z")
	if e != nil {
		return e
	}
	if paths != "" {
		o.report.Conflicts = strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00")
	}
	if o.record == nil {
		o.report.Phase = "none"
		return nil
	}
	r := o.record
	o.report.BaseCommit = r.BaseCommit
	o.report.StashOID = r.StashOID
	o.report.Phase = r.Phase
	o.report.ChangesRestored = r.ChangesRestored
	if r.HadChanges && r.StashOID == "" {
		s, e := o.findStash()
		if e != nil {
			return e
		}
		o.report.StashOID = s
	}
	o.report.Confirmed = o.report.BranchConfirmed && (r.Phase == "created" || r.Phase == "restored") && (!r.HadChanges || r.ChangesRestored)
	return nil
}
func (o *operation) sourceUnchanged() error {
	b, h, e := o.identity()
	if e != nil {
		return e
	}
	if b != o.record.SourceBranch || h != o.record.SourceHead {
		return failure("identity_mismatch", "source checkout changed during branch creation", nil)
	}
	return nil
}
