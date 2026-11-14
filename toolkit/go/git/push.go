package git

import (
	"context"
	"errors"
	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// PushOptions identifies the exact local commit and remote branch. History rewriting belongs to rebase.
type PushOptions struct {
	Branch      string
	Commit      string // Required full object ID; publication checks it still names the local branch tip.
	Remote      string
	Target      string // Defaults to Branch.
	SetUpstream bool
	Check       bool // Observe the frozen commit at the destination without writes, even if the local branch moved.
}
type PushPhase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type PushDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type PushReport struct {
	Checkout          string           `json:"checkout"`
	Branch            string           `json:"branch"`
	Target            string           `json:"target"`
	Remote            string           `json:"remote"`
	TargetCommit      string           `json:"targetCommit"`
	RemoteCommit      string           `json:"remoteCommit,omitempty"`
	RemoteState       string           `json:"remoteState"` // matching, different, missing, unknown
	Check             bool             `json:"check"`
	PushAttempted     bool             `json:"pushAttempted"`
	PushExitCode      *int             `json:"pushExitCode,omitempty"`
	Confirmed         bool             `json:"confirmed"` // The destination was observed at TargetCommit; upstream configuration is separate.
	UpstreamRequested bool             `json:"upstreamRequested"`
	UpstreamConfirmed bool             `json:"upstreamConfirmed"`
	Diagnostics       []PushDiagnostic `json:"diagnostics"`
	Phases            []PushPhase      `json:"phases"`
}

// Push publishes a frozen branch commit without force and verifies the single push destination.
// +spec=Only matching remote evidence confirms publication. Never retry a push automatically or replace the requested commit with a newer branch tip; check observes the original intent.
// +link=component://docs/git-push.md
func Push(ctx context.Context, repo string, opts PushOptions) (PushReport, error) {
	return push(ctx, repo, opts, command.Run)
}
func push(ctx context.Context, repo string, opts PushOptions, run gitRunner) (report PushReport, err error) {
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	if opts.Target == "" {
		opts.Target = opts.Branch
	}
	report = PushReport{Checkout: repo, Branch: opts.Branch, Target: opts.Target, Remote: opts.Remote, TargetCommit: opts.Commit, RemoteState: "unknown", Check: opts.Check, UpstreamRequested: opts.SetUpstream, Diagnostics: []PushDiagnostic{}, Phases: []PushPhase{}}
	defer func() {
		err = errs.Wrap(err, "git.push_failed", "git.push", opts.Remote+"/"+opts.Target)
		slog.InfoContext(ctx, "Branch publication", "checkout", report.Checkout, "branch", opts.Branch, "remote", opts.Remote, "target", opts.Target, "commit", opts.Commit, "check", opts.Check, "push_attempted", report.PushAttempted, "confirmed", report.Confirmed, "failed", err != nil)
	}()
	measure := func(name string, fn func() error) error {
		start := time.Now()
		e := fn()
		report.Phases = append(report.Phases, PushPhase{name, time.Since(start).Seconds()})
		return e
	}
	query := func(timeout time.Duration, args ...string) (string, error) {
		r := run(ctx, repo, timeout, args...)
		if r.Err != nil {
			return "", &errs.Error{Code: "git.query_failed", Message: "Git observation failed during branch publication", Cause: r.Err}
		}
		return strings.TrimSuffix(r.Stdout, "\n"), nil
	}
	failure := func(code, msg string, cause error) error {
		return &errs.Error{Code: "git.push_" + code, Message: msg, Cause: cause}
	}
	var info worktree.CheckoutInfo
	err = measure("prepare", func() error {
		if !repogit.ValidObjectID(opts.Commit) || (opts.Check && opts.SetUpstream) {
			return failure("invalid_argument", "push requires a full commit ID; --check cannot configure upstream", nil)
		}
		if e := repogit.ValidateBranch(ctx, repo, opts.Branch); e != nil {
			return failure("invalid_argument", "explicit valid source branch is required", e)
		}
		if e := repogit.ValidateBranch(ctx, repo, opts.Target); e != nil {
			return failure("invalid_argument", "valid destination branch is required", e)
		}
		var e error
		info, e = worktree.Local(ctx, repo)
		if e == nil {
			repo = info.Root
			report.Checkout = repo
		}
		return e
	})
	if err != nil {
		return report, err
	}
	var unlock func()
	err = measure("lock_wait", func() error {
		var e error
		unlock, e = repogit.AcquireLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
		return e
	})
	if err != nil {
		return report, err
	}
	defer unlock()
	checkSource := func() error {
		head, exists, e := repogit.BranchCommit(ctx, repo, opts.Branch)
		if e != nil {
			return e
		}
		if !exists || head != opts.Commit {
			return failure("source_changed", "local branch no longer matches the requested commit; no push attempted", nil)
		}
		return nil
	}
	var endpoint string
	ref := "refs/heads/" + opts.Target
	observe := func() error {
		report.RemoteCommit = ""
		report.RemoteState = "unknown"
		report.Confirmed = false
		raw, e := query(30*time.Second, "ls-remote", "--refs", "--", endpoint, ref)
		if e != nil {
			return e
		}
		tip, e := repogit.BranchTip(raw, ref)
		if e != nil {
			return e
		}
		report.RemoteCommit = tip
		switch tip {
		case opts.Commit:
			report.RemoteState = "matching"
			report.Confirmed = true
		case "":
			report.RemoteState = "missing"
		default:
			report.RemoteState = "different"
		}
		return nil
	}
	err = measure("remote_preflight", func() error {
		var e error
		endpoint, e = repogit.PushDestination(repo, opts.Remote, query)
		if e != nil {
			return e
		}
		if !opts.Check {
			if e = checkSource(); e != nil {
				return e
			}
		}
		return observe()
	})
	if err != nil {
		return report, err
	}
	if opts.Check {
		return report, nil
	}
	if !report.Confirmed {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		if err = checkSource(); err != nil {
			return report, err
		}
		var result command.Result
		_ = measure("push", func() error {
			report.PushAttempted = true
			result = run(ctx, repo, repogit.PushBudget(ctx), "push", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--", endpoint, opts.Commit+":"+ref)
			report.PushExitCode = &result.ExitCode
			return result.Err
		})
		err = measure("remote_verify", observe)
		if !report.Confirmed {
			if err == nil && !result.Uncertain && repogit.PushRejected(result.Stdout, ref) {
				return report, failure("rejected", "remote rejected branch publication; inspect the observed remote commit", result.Err)
			}
			return report, &errs.Error{Code: "git.push_unknown", Message: "branch publication could not be confirmed", Uncertain: true, Cause: errors.Join(result.Err, err), Hint: "Observe the same remote and target with --check and the original --commit before deciding whether to retry; no push was retried."}
		}
		if result.Err != nil {
			report.Diagnostics = append(report.Diagnostics, PushDiagnostic{"git.push_reconciled", "Push response failed, but the requested commit was confirmed at the remote destination"})
		}
	}
	if opts.SetUpstream {
		e := measure("configure_upstream", func() error {
			if e := checkSource(); e != nil {
				return e
			}
			for _, entry := range [][2]string{{"branch." + opts.Branch + ".remote", opts.Remote}, {"branch." + opts.Branch + ".merge", ref}} {
				r := run(ctx, repo, 5*time.Second, "config", "--local", "--replace-all", entry[0], entry[1])
				if r.Err != nil {
					return r.Err
				}
			}
			for _, entry := range [][2]string{{"branch." + opts.Branch + ".remote", opts.Remote}, {"branch." + opts.Branch + ".merge", ref}} {
				got, e := query(5*time.Second, "config", "--local", "--get", entry[0])
				if e != nil {
					return e
				}
				if got != entry[1] {
					return failure("upstream_mismatch", "upstream configuration did not match the requested branch", nil)
				}
			}
			return nil
		})
		if e == nil {
			e = measure("refresh_tracking", func() error {
				result := run(ctx, repo, 30*time.Second, "fetch", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--", opts.Remote, ref)
				if result.Err != nil {
					return result.Err
				}
				tracked, err := query(5*time.Second, "rev-parse", "--verify", "--end-of-options", opts.Branch+"@{upstream}^{commit}")
				if err != nil {
					return err
				}
				if tracked != opts.Commit {
					return failure("upstream_mismatch", "upstream tracking differs from the published commit", nil)
				}
				report.UpstreamConfirmed = true
				return nil
			})
		}
		if e != nil {
			report.Diagnostics = append(report.Diagnostics, PushDiagnostic{"git.upstream_unconfirmed", "Remote commit confirmed, but upstream tracking could not be confirmed at that commit; inspect local tracking settings and the fetch destination"})
			slog.WarnContext(ctx, "Upstream configuration unconfirmed", "checkout", repo, "branch", opts.Branch, "remote", opts.Remote)
		}
	}
	return report, nil
}
