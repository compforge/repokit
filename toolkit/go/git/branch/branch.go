// Package branch creates explicit branches while preserving local changes and recovery evidence.
package branch

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

type Options struct {
	Name, Base, Remote string
	CarryChanges       bool
}
type Phase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Report struct {
	Checkout        string       `json:"checkout"`
	Branch          string       `json:"branch"`
	BaseCommit      string       `json:"baseCommit,omitempty"`
	CurrentBranch   string       `json:"currentBranch"`
	Head            string       `json:"headSha,omitempty"`
	Action          string       `json:"action"` // created, reused, observed
	Phase           string       `json:"phase"`
	StashOID        string       `json:"stashOid,omitempty"`
	RecoveryPath    string       `json:"recoveryPath,omitempty"`
	BranchConfirmed bool         `json:"branchConfirmed"`
	ChangesRestored bool         `json:"changesRestored"`
	Confirmed       bool         `json:"confirmed"`
	WriteAttempted  bool         `json:"writeAttempted"`
	Conflicts       []string     `json:"conflicts"`
	Diagnostics     []Diagnostic `json:"diagnostics"`
	Phases          []Phase      `json:"phases"`
}
type runner func(context.Context, string, time.Duration, ...string) gitworktree.GitResult
type operation struct {
	ctx    context.Context
	repo   string
	info   gitworktree.CheckoutInfo
	run    runner
	record *record
	report Report
}

// Create creates and switches to an explicit branch; carry preserves the index and untracked files.
// +spec=Never reset an existing branch, pop a positional stash, or repeat an unconfirmed stash apply. A created branch and restored changes are separate facts.
// +link=component://docs/git-branch.md
func Create(ctx context.Context, repo string, opts Options) (Report, error) {
	return execute(ctx, repo, opts, false, gitworktree.Run)
}

// Status observes recovery evidence and live Git identity without replaying writes.
func Status(ctx context.Context, repo, name string) (Report, error) {
	return execute(ctx, repo, Options{Name: name}, true, gitworktree.Run)
}
func execute(ctx context.Context, repo string, opts Options, observe bool, run runner) (report Report, err error) {
	o := &operation{ctx: ctx, repo: repo, run: run, report: Report{Checkout: repo, Branch: opts.Name, Phase: "unknown", Conflicts: []string{}, Diagnostics: []Diagnostic{}, Phases: []Phase{}}}
	defer func() {
		report = o.report
		action := "create"
		if observe {
			action = "status"
		}
		err = errs.Wrap(err, "git.branch_failed", "git.branch."+action, repo)
		if !observe || err != nil {
			slog.InfoContext(ctx, "Branch operation", "checkout", report.Checkout, "branch", opts.Name, "phase", report.Phase, "branch_confirmed", report.BranchConfirmed, "changes_restored", report.ChangesRestored, "write_attempted", report.WriteAttempted, "failed", err != nil)
		}
	}()
	if e := gitworktree.ValidateBranch(ctx, repo, opts.Name); e != nil {
		return report, failure("invalid_argument", "explicit valid branch name is required", e)
	}
	o.info, err = gitworktree.Local(ctx, repo)
	if err != nil {
		return report, err
	}
	o.repo = o.info.Root
	o.report.Checkout = o.repo
	var unlock func()
	err = o.measure("lock_wait", func() error {
		var e error
		unlock, e = gitworktree.AcquireLease(ctx, filepath.Join(o.info.CommonDir, "repokit-worktrees.lock"))
		return e
	})
	if err != nil {
		return report, err
	}
	defer unlock()
	o.report.RecoveryPath = recordPath(o.info.GitDir, opts.Name)
	err = o.measure("inspect", func() error {
		var e error
		o.record, e = readRecord(o.report.RecoveryPath, opts.Name)
		if e != nil {
			return e
		}
		return o.observe()
	})
	if err != nil {
		return report, err
	}
	if observe {
		o.report.Action = "observed"
		return report, nil
	}
	err = o.create(opts)
	return report, err
}
func (o *operation) measure(name string, fn func() error) error {
	start := time.Now()
	defer func() { o.report.Phases = append(o.report.Phases, Phase{name, time.Since(start).Seconds()}) }()
	return fn()
}
func failure(code, msg string, cause error) error {
	return &errs.Error{Code: "git.branch_" + code, Message: msg, Cause: cause}
}
func unknown(msg string, cause error) error {
	return &errs.Error{Code: "git.branch_unknown", Message: msg, Cause: cause, Uncertain: true, Hint: "Inspect git branch status for this branch. Keep the recovery record and stash; do not replay stash apply automatically."}
}
func (o *operation) save(phase string) error {
	o.record.Phase = phase
	o.report.Phase = phase
	o.report.StashOID = o.record.StashOID
	return gitworktree.WriteRecord(o.report.RecoveryPath, o.record)
}
func (o *operation) complete(phase string) {
	if e := o.save(phase); e != nil {
		o.report.Diagnostics = append(o.report.Diagnostics, Diagnostic{"git.branch_record_failed", "Recovery record publication failed; inspect Git effects and retained stash before retrying"})
		slog.WarnContext(o.ctx, "Branch recovery record publication failed", "checkout", o.repo, "branch", o.report.Branch, "phase", phase, "error", e)
	}
}
