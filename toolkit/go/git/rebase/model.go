// Package rebase owns recoverable Git history rewrites and exact-lease publication.
// Caller workflow, sessions, PR policy and validation remain outside this package.
package rebase

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

type Action string

const (
	Start    Action = "start"
	Status   Action = "status"
	Continue Action = "continue"
	Abort    Action = "abort"
	Finish   Action = "finish"
)

type Options struct{ Branch, Target, Remote string }

// Transaction keeps immutable rewrite inputs and the publication intent across invocations.
// +spec=The remote lease is captured before rewriting; publication freezes a commit before any push and never refreshes that lease.
// +link=component://docs/git-rebase.md
type Transaction struct {
	Branch         string    `json:"branch"`
	Target         string    `json:"target"`
	Remote         string    `json:"remote"`
	StartedHead    string    `json:"startedHead"`
	TargetCommit   string    `json:"targetCommit"`
	ExpectedRemote string    `json:"expectedRemote"`
	PublishHead    string    `json:"publishHead,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
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
	Checkout       string       `json:"checkout"`
	Action         Action       `json:"action"`
	Phase          string       `json:"phase"` // none, unmanaged, prepared, paused, ready, publishing, published, aborted, failed, unknown
	Transaction    *Transaction `json:"transaction,omitempty"`
	Head           string       `json:"headSha,omitempty"`
	Conflicts      []string     `json:"conflicts"`
	WriteAttempted bool         `json:"writeAttempted"`
	PushAttempted  bool         `json:"pushAttempted"`
	Confirmed      bool         `json:"confirmed"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	Phases         []Phase      `json:"phases"`
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

// Run advances or observes one checkout's rebase; a paused conflict is a confirmed state,
// not completion. Finish alone publishes, after the caller's validation and authorization.
// +spec=Unknown writes retain recovery evidence. A remote already equal to the frozen publish commit completes without another push.
// +link=component://docs/git-rebase.md
func Run(ctx context.Context, repo string, action Action, opts Options) (Report, error) {
	return execute(ctx, repo, action, opts, gitworktree.Run)
}
func execute(ctx context.Context, repo string, action Action, opts Options, run runner) (report Report, err error) {
	o := &operation{ctx: ctx, repo: repo, run: run, report: Report{Checkout: repo, Action: action, Phase: "unknown", Conflicts: []string{}, Diagnostics: []Diagnostic{}, Phases: []Phase{}}}
	defer func() {
		report = o.report
		err = errs.Wrap(err, "git.rebase_failed", "git.rebase."+string(action), repo)
		if action != Status || err != nil {
			slog.InfoContext(ctx, "Rebase operation", "action", action, "checkout", report.Checkout, "phase", report.Phase, "confirmed", report.Confirmed, "write_attempted", report.WriteAttempted, "push_attempted", report.PushAttempted, "failed", err != nil)
		}
	}()
	if action != Start && action != Status && action != Continue && action != Abort && action != Finish {
		return report, failure("invalid_argument", "unknown rebase action", nil)
	}
	o.info, err = gitworktree.Local(ctx, repo)
	if err != nil {
		return report, err
	}
	o.repo, o.report.Checkout = o.info.Root, o.info.Root
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
	err = o.measure("inspect", func() error {
		var e error
		o.record, e = readRecord(o.info.GitDir)
		if e != nil {
			return e
		}
		return o.observe()
	})
	if err != nil {
		return report, err
	}
	switch action {
	case Status:
		o.report.Confirmed = true
		return report, nil
	case Start:
		err = o.start(opts)
	case Continue:
		err = o.continueRebase()
	case Abort:
		err = o.abort()
	case Finish:
		err = o.finish()
	}
	return report, err
}
func (o *operation) measure(name string, fn func() error) error {
	start := time.Now()
	defer func() { o.report.Phases = append(o.report.Phases, Phase{name, time.Since(start).Seconds()}) }()
	return fn()
}
func failure(code, message string, cause error) error {
	return &errs.Error{Code: "git.rebase_" + code, Message: message, Cause: cause}
}
func unknown(message string, cause error) error {
	return &errs.Error{Code: "git.rebase_unknown", Message: message, Cause: cause, Uncertain: true, Hint: "Inspect git rebase status in this checkout before deciding how to recover; no write was retried."}
}
func (o *operation) publishState(phase string) {
	o.record.Phase, o.report.Phase = phase, phase
	if err := writeRecord(o.info.GitDir, o.record); err != nil {
		o.report.Diagnostics = append(o.report.Diagnostics, Diagnostic{"git.rebase_record_failed", "Git outcome confirmed, but recovery record publication failed; inspect status before the next action"})
		slog.WarnContext(o.ctx, "Rebase record publication failed", "checkout", o.repo, "phase", phase)
	}
}
