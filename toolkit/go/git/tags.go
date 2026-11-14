// Package git provides explicit repository operations without workflow or session policy.
package git

import (
	"context"
	"errors"
	"fmt"
	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"log/slog"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

type PublishTagsOptions struct {
	Remote string
	Target string
	Tags   []string
}

type TagObservation struct {
	Name     string `json:"name"`
	Ref      string `json:"ref"`
	ObjectID string `json:"objectID,omitempty"`
	Status   string `json:"status"` // present, missing, conflict, unknown
}

type TagPublishPhase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}

type TagPublishReport struct {
	Remote        string            `json:"remote"`
	TargetCommit  string            `json:"targetCommit"`
	Tags          []TagObservation  `json:"tags"`
	PushAttempted bool              `json:"pushAttempted"`
	PushExitCode  *int              `json:"pushExitCode,omitempty"`
	Confirmed     bool              `json:"confirmed"`
	Phases        []TagPublishPhase `json:"phases"`
}

type gitRunner func(context.Context, string, time.Duration, ...string) command.Result

// PublishTags publishes missing lightweight tags at one resolved commit. Existing annotated
// tags are compared by their peeled commit and are never replaced.
// +spec=Preflight and reconciliation query the same single push destination; missing tags use one atomic non-force push, never an automatic retry.
// +link=component://docs/git.md#tag-publication
func PublishTags(ctx context.Context, repo string, opts PublishTagsOptions) (TagPublishReport, error) {
	return publishTags(ctx, repo, opts, command.Run)
}

func publishTags(ctx context.Context, repo string, opts PublishTagsOptions, run gitRunner) (report TagPublishReport, err error) {
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	report = TagPublishReport{Remote: opts.Remote, Tags: []TagObservation{}, Phases: []TagPublishPhase{}}
	defer func() {
		err = errs.Wrap(err, "git.tag_publish_failed", "git.tag.publish", opts.Remote)
		if err != nil {
			slog.WarnContext(ctx, "Tag publication incomplete", "remote", opts.Remote, "target", report.TargetCommit, "push_attempted", report.PushAttempted)
		} else {
			slog.InfoContext(ctx, "Tags confirmed", "remote", opts.Remote, "target", report.TargetCommit, "count", len(report.Tags), "push_attempted", report.PushAttempted)
		}
	}()
	phase := func(name string, action func() error) error {
		start := time.Now()
		e := action()
		report.Phases = append(report.Phases, TagPublishPhase{Name: name, DurationSeconds: time.Since(start).Seconds()})
		return e
	}
	failure := func(code, message string, cause error) error {
		return &errs.Error{Code: code, Message: message, Cause: cause}
	}
	query := func(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
		r := run(ctx, repo, timeout, args...)
		if r.Err != nil {
			return "", failure("git.query_failed", "Git "+args[0]+" query failed during tag publication", r.Err)
		}
		return strings.TrimSuffix(r.Stdout, "\n"), nil
	}
	var endpoint string
	err = phase("prepare", func() error {
		if opts.Target == "" || len(opts.Tags) == 0 || strings.HasPrefix(opts.Remote, "-") {
			return failure("git.invalid_argument", "tag publication requires tags, an explicit target and a configured remote", nil)
		}
		var e error
		endpoint, e = repogit.PushDestination(repo, opts.Remote, func(timeout time.Duration, args ...string) (string, error) { return query(ctx, timeout, args...) })
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, name := range opts.Tags {
			if name == "" {
				return failure("git.invalid_argument", "tag name must not be empty", nil)
			}
			ref := "refs/tags/" + name
			if _, e = query(ctx, 5*time.Second, "check-ref-format", ref); e != nil {
				return failure("git.invalid_argument", fmt.Sprintf("invalid tag name %q", name), e)
			}
			if !seen[name] {
				report.Tags = append(report.Tags, TagObservation{Name: name, Ref: ref, Status: "unknown"})
				seen[name] = true
			}
		}
		report.TargetCommit, e = query(ctx, 5*time.Second, "rev-parse", "--verify", "--end-of-options", opts.Target+"^{commit}")
		return e
	})
	if err != nil {
		return report, err
	}
	observe := func() error {
		args := []string{"ls-remote", "--", endpoint}
		for _, tag := range report.Tags {
			args = append(args, tag.Ref, tag.Ref+"^{}")
		}
		output, e := query(ctx, 30*time.Second, args...)
		if e != nil {
			return e
		}
		refs := map[string]string{}
		for _, line := range strings.Split(output, "\n") {
			if line == "" {
				continue
			}
			sha, ref, ok := strings.Cut(line, "\t")
			if !ok || sha == "" || ref == "" {
				return failure("git.invalid_response", "invalid remote ref observation", nil)
			}
			refs[ref] = sha
		}
		report.Confirmed = true
		for i := range report.Tags {
			tag := &report.Tags[i]
			tag.ObjectID = refs[tag.Ref]
			if peeled := refs[tag.Ref+"^{}"]; peeled != "" {
				tag.ObjectID = peeled
			}
			switch tag.ObjectID {
			case report.TargetCommit:
				tag.Status = "present"
			case "":
				tag.Status = "missing"
			default:
				tag.Status = "conflict"
			}
			report.Confirmed = report.Confirmed && tag.Status == "present"
		}
		return nil
	}
	if err = phase("remote_preflight", observe); err != nil {
		return report, err
	}
	for _, tag := range report.Tags {
		if tag.Status == "conflict" {
			return report, failure("git.tag_conflict", fmt.Sprintf("refusing to move tag %s from %s to %s", tag.Name, tag.ObjectID, report.TargetCommit), nil)
		}
	}
	if report.Confirmed {
		return report, nil
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	args := []string{"push", "--atomic", "--no-follow-tags", "--porcelain", "--", endpoint}
	for _, tag := range report.Tags {
		if tag.Status == "missing" {
			args = append(args, report.TargetCommit+":"+tag.Ref)
		}
	}
	var pushed command.Result
	_ = phase("push", func() error {
		// +why=Reserve part of the caller's remaining budget for reconciliation; never extend its deadline.
		budget := repogit.PushBudget(ctx)
		report.PushAttempted = true
		pushed = run(ctx, repo, budget, args...)
		report.PushExitCode = &pushed.ExitCode
		return pushed.Err
	})
	err = phase("remote_verify", observe)
	if err != nil {
		report.Confirmed = false
		for i := range report.Tags {
			report.Tags[i].Status = "unknown"
			report.Tags[i].ObjectID = ""
		}
	} else if report.Confirmed {
		if pushed.Err != nil {
			slog.WarnContext(ctx, "Tag push error reconciled by remote observation", "remote", opts.Remote, "target", report.TargetCommit, "exit_code", pushed.ExitCode)
		}
		return report, nil
	}
	if err != nil || pushed.Uncertain || !pushRejected(pushed.Stdout, report.Tags) {
		return report, &errs.Error{Code: "git.tag_publish_unknown", Message: "remote tags could not be confirmed after push", Uncertain: true, Cause: errors.Join(pushed.Err, err), Hint: "Inspect the requested tags at this remote's push destination before deciding whether to retry; no push was retried."}
	}
	return report, failure("git.tag_publish_failed", "remote rejected tag publication; inspect the reported tag states", pushed.Err)
}

// Git porcelain distinguishes a definite rejection from an unacknowledged remote failure.
// Unknown/localized summaries stay uncertain; never infer rejection from free-form stderr.
func pushRejected(output string, tags []TagObservation) bool {
	for _, tag := range tags {
		if repogit.PushRejected(output, tag.Ref) {
			return true
		}
	}
	return false
}
