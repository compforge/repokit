package commit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

// Plan prepares candidate content without changing HEAD, the real index or working files.
// Git clean filters may run and Git objects may be written; this is an explicit preparation.
func Plan(ctx context.Context, repo string, selection Selection) (Report, error) {
	return execute(ctx, repo, "plan", Options{Intent: Intent{Selection: selection}})
}

// Create commits only after rechecking the planned HEAD, index and candidate tree.
// +spec=Native hooks run against the candidate index. Only the expected parent and tree confirm a commit; failures never reset user work or automatically replay a write.
// +link=component://docs/git-commit.md
func Create(ctx context.Context, repo string, opts Options) (Report, error) {
	return execute(ctx, repo, "create", opts)
}

// Check observes the current tip against the original intent, without staging or committing.
func Check(ctx context.Context, repo string, intent Intent) (Report, error) {
	return execute(ctx, repo, "check", Options{Intent: intent})
}

func execute(ctx context.Context, repo, action string, opts Options) (report Report, err error) {
	report = Report{Intent: opts.Intent, Checkout: repo, Action: action, Changes: []Change{}, Parents: []string{}, Diagnostics: []Diagnostic{}, Phases: []Phase{}}
	defer func() {
		err = errs.Wrap(err, "git.commit_failed", "git.commit."+action, report.Checkout)
		slog.InfoContext(ctx, "Commit operation", "checkout", report.Checkout, "action", action, "branch", report.Branch, "commit", report.Commit, "attempted", report.Attempted, "confirmed", report.Confirmed, "failed", err != nil)
	}()
	measure := func(name string, fn func() error) error {
		start := time.Now()
		e := fn()
		report.Phases = append(report.Phases, Phase{name, time.Since(start).Seconds()})
		return e
	}
	if action != "plan" {
		if !gitworktree.ValidObjectID(opts.Tree) || (opts.Head != "unborn" && !gitworktree.ValidObjectID(opts.Head)) || opts.Branch == "" {
			return report, failure("intent", "an explicit branch, expected parent and tree are required")
		}
		if action == "create" && (opts.IndexFingerprint == "" || strings.TrimSpace(opts.Message) == "" || strings.ContainsRune(opts.Message, 0)) {
			return report, failure("intent", "create requires the planned index fingerprint and a nonempty message without NUL")
		}
	}
	info, e := gitworktree.Local(ctx, repo)
	if e != nil {
		return report, e
	}
	report.Checkout = info.Root
	o := operation{ctx: ctx, info: info, report: &report}
	if action == "check" {
		err = measure("observe", func() error { return o.observe(opts.Intent) })
		return report, err
	}
	var unlock func()
	if err = measure("lock_wait", func() error {
		var e error
		unlock, e = gitworktree.AcquireLease(ctx, filepath.Join(info.CommonDir, "repokit-worktrees.lock"))
		return e
	}); err != nil {
		return report, err
	}
	defer unlock()
	// Native index writers must not overwrite or race the candidate's installation.
	index := filepath.Join(info.GitDir, "index")
	lock := index + ".lock"
	f, e := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return report, e
	}
	defer func() {
		_ = f.Close()
		// After rename the lock path is available to other Git writers; never remove their lock.
		if !report.IndexInstalled {
			_ = os.Remove(lock)
		}
	}()
	dir, e := os.MkdirTemp(info.GitDir, "repokit-commit-")
	if e != nil {
		return report, e
	}
	retain := false
	defer func() {
		if !retain {
			_ = os.RemoveAll(dir)
		}
	}()
	o.index = filepath.Join(dir, "index")
	if err = measure("prepare", func() error { return o.prepare(opts.Selection) }); err != nil {
		return report, err
	}
	if action == "plan" {
		return report, nil
	}
	if report.Branch != opts.Branch || report.Head != opts.Head || report.Tree != opts.Tree || report.IndexFingerprint != opts.IndexFingerprint {
		return report, failure("changed", "branch, HEAD, index or selected content changed since planning; no commit attempted")
	}
	if report.Empty {
		return report, nil
	}
	// Keep the original intent beside the candidate before any potentially uncertain write.
	data, e := json.MarshalIndent(opts.Intent, "", "  ")
	if e != nil {
		return report, e
	}
	if e = os.WriteFile(filepath.Join(dir, "intent.json"), data, 0600); e != nil {
		return report, e
	}
	if e = os.WriteFile(filepath.Join(dir, "message"), []byte(opts.Message), 0600); e != nil {
		return report, e
	}
	b, h, e := o.identity()
	if e != nil {
		return report, e
	}
	if b != opts.Branch || h != opts.Head {
		return report, failure("changed", "branch or HEAD changed before commit")
	}
	if e = ctx.Err(); e != nil {
		return report, e
	}
	retain = true
	report.RecoveryPath = dir
	report.Attempted = true
	budget := 2 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		reserve := min(5*time.Second, remaining/4)
		budget = remaining - reserve
	}
	var result gitworktree.GitResult
	_ = measure("commit", func() error {
		result = gitworktree.RunIndex(ctx, info.Root, o.index, budget, "commit", "--file", filepath.Join(dir, "message"))
		return result.Err
	})
	err = measure("verify", func() error { return o.observe(opts.Intent) })
	if !report.Confirmed {
		if err != nil || result.Uncertain {
			return report, &errs.Error{Code: "git.commit_unknown", Message: "commit result could not be confirmed", Uncertain: true, Cause: errors.Join(result.Err, err), Hint: "Inspect the retained candidate and use git commit check with the original branch, head and tree before retrying."}
		}
		if report.Commit != "" {
			return report, failure("mismatch", "branch advanced but its commit does not match the planned parent and tree; inspect actual commit and retained candidate before proceeding")
		}
		if result.Err != nil {
			return report, result.Err
		}
		return report, failure("unconfirmed", "Git exited successfully without the expected branch commit")
	}
	if result.Err != nil {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{"git.commit_reconciled", "Git response failed, but the planned parent and tree were confirmed in the branch commit"})
	}
	// A confirmed commit is never retracted by a later local index installation failure.
	err = measure("install_index", func() error {
		digest, _, e := fingerprint(index)
		if e != nil {
			return e
		}
		if digest != opts.IndexFingerprint {
			return failure("index_changed", "real index changed despite its writer lock")
		}
		candidate, e := os.ReadFile(o.index)
		if e != nil {
			return e
		}
		if _, e = f.Write(candidate); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		return os.Rename(lock, index)
	})
	if err != nil {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{"git.commit_index_uninstalled", "Commit confirmed; index installation failed. Inspect the retained candidate before any further commit."})
		return report, nil
	}
	report.IndexInstalled = true
	retain = false
	report.RecoveryPath = ""
	return report, nil
}
func (o *operation) observe(intent Intent) error {
	branch, tip, e := o.identity()
	if e != nil {
		return e
	}
	if branch != intent.Branch {
		return failure("branch_changed", "checkout branch changed; observe the original branch before proceeding")
	}
	if tip == "unborn" || tip == intent.Head {
		return nil
	}
	o.report.Commit = tip
	raw, e := o.query("show", "--no-patch", "--format=%T%n%P", tip, "--")
	if e != nil {
		return e
	}
	tree, parents, _ := strings.Cut(raw, "\n")
	if !gitworktree.ValidObjectID(tree) {
		return failure("evidence", "invalid commit tree evidence")
	}
	o.report.ActualTree = tree
	o.report.Parents = strings.Fields(parents)
	parentMatches := (intent.Head == "unborn" && len(o.report.Parents) == 0) || (len(o.report.Parents) == 1 && o.report.Parents[0] == intent.Head)
	o.report.Confirmed = parentMatches && tree == intent.Tree
	return nil
}
