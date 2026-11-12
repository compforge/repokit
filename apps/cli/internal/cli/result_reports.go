package cli

import (
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/stats"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/depot"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/forge/discussion"
	"github.com/compforge/repokit/toolkit/go/forge/release"
	repogit "github.com/compforge/repokit/toolkit/go/git"
	"github.com/compforge/repokit/toolkit/go/git/branch"
	commitgit "github.com/compforge/repokit/toolkit/go/git/commit"
	"github.com/compforge/repokit/toolkit/go/git/rebase"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/compforge/repokit/toolkit/go/worktree"
)

// describeResult projects public domain facts; it never infers failure from log text.
// Analysis completeness remains distinct from successful command execution.
func describeResult(r *commandResult, data any) {
	r.Outcome = "succeeded"
	switch v := data.(type) {
	case depot.Report:
		r.Summary = fmt.Sprintf("%d Component(s), %d Bin(s); complete=%t", len(v.Components), len(v.Bins), v.Complete)
		for _, p := range v.Phases {
			name := "depot." + p.Name
			if p.Bin != "" {
				name += "/" + p.Bin
			}
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: name, DurationSeconds: p.DurationSeconds})
		}
		for i, b := range v.Bins {
			if b.Status == "present" {
				r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "depot.unverified_environment", Severity: "warning", Phase: "depot", Path: b.Path, Message: b.Reason, Evidence: fmt.Sprintf("/data/bins/%d", i)})
			}
		}
	case release.QueryResult:
		r.Summary = "No Release found"
		if v.Release != nil {
			r.Summary = fmt.Sprintf("Release %s at %s", v.Release.Tag, v.Release.TargetCommit)
		}
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "release." + p.Name, DurationSeconds: p.DurationSeconds})
		}
	case release.PublishResult:
		r.Summary = fmt.Sprintf("Release %s; confirmed=%t; writeAttempted=%t", v.Tag, v.Confirmed, v.WriteAttempted)
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "release." + p.Name, DurationSeconds: p.DurationSeconds})
		}
	case branch.Report:
		r.Summary = fmt.Sprintf("Branch %s: %s; branchConfirmed=%t; changesRestored=%t", v.Branch, v.Phase, v.BranchConfirmed, v.ChangesRestored)
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "git.branch." + p.Name, DurationSeconds: p.DurationSeconds})
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "branch", Path: v.Checkout, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case rebase.Report:
		r.Summary = fmt.Sprintf("Rebase %s: %s; confirmed=%t", v.Action, v.Phase, v.Confirmed)
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "git.rebase." + p.Name, DurationSeconds: p.DurationSeconds})
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "rebase", Path: v.Checkout, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case commitgit.Report:
		r.Summary = fmt.Sprintf("Commit %s: %s; confirmed=%t; indexInstalled=%t", v.Action, v.Commit, v.Confirmed, v.IndexInstalled)
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "git.commit." + p.Name, DurationSeconds: p.DurationSeconds})
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "commit", Path: v.Checkout, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case repogit.PushReport:
		r.Summary = fmt.Sprintf("Push %s to %s/%s: %s; confirmed=%t; pushAttempted=%t", v.Branch, v.Remote, v.Target, v.RemoteState, v.Confirmed, v.PushAttempted)
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "git.push." + p.Name, DurationSeconds: p.DurationSeconds})
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "push", Path: v.Checkout, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case repogit.TagPublishReport:
		r.Summary = fmt.Sprintf("%d tags on %s; confirmed=%t", len(v.Tags), v.Remote, v.Confirmed)
		for _, phase := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "git.tag." + phase.Name, DurationSeconds: phase.DurationSeconds})
		}
	case repokit.InspectReport:
		r.Summary = fmt.Sprintf("Inspected %d Components; complete=%t", len(v.Components), v.Complete)
		r.analysisDiagnostics(v.Diagnostics, "inspect", "/data/diagnostics")
	case repokit.TreeReport:
		r.Summary = fmt.Sprintf("Listed %d files; complete=%t", len(v.Files), v.Complete)
		r.analysisDiagnostics(v.Diagnostics, "tree", "/data/diagnostics")
	case repokit.SnapshotReport:
		r.Summary = fmt.Sprintf("Captured %d files; complete=%t", v.FileCount, v.Complete)
		r.analysisDiagnostics(v.Diagnostics, "snapshot", "/data/diagnostics")
	case repokit.ImpactReport:
		r.Summary = fmt.Sprintf("%d changed files; impact scope=%s; complete=%t", len(v.Changes), v.Scope, v.Complete)
		r.analysisDiagnostics(v.Diagnostics, "impact", "/data/diagnostics")
	case repokit.DiffReport:
		r.Summary = fmt.Sprintf("%d changed files; complete=%t", len(v.Changes), v.Complete)
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "diff", Path: d.Path, Version: d.Snapshot, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case repokit.UnitReport:
		r.Summary = fmt.Sprintf("%d fragments, %d units; complete=%t; limitExceeded=%t", len(v.Fragments), len(v.Units), v.Complete, v.LimitExceeded)
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "units", Path: d.Path, Version: d.Snapshot, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
		if v.LimitExceeded {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "units.limit_exceeded", Severity: "warning", Phase: "units", Message: "Unit count exceeds the soft limit; all formed units are retained", Evidence: "/data/limit_exceeded"})
		}
	case repokit.DeadcodeReport:
		r.analysisDiagnostics(v.Scope.Diagnostics, "deadcode_scope", "/data/scope/diagnostics")
		if v.Baseline != nil {
			r.analysisDiagnostics(v.Baseline.Snapshot.Diagnostics, "before_snapshot", "/data/baseline/snapshot/diagnostics")
			for i, d := range v.Baseline.Diagnostics {
				r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "before_graph", Message: d.Message, Path: d.Location.Path, Line: d.Location.Line, Evidence: fmt.Sprintf("/data/baseline/diagnostics/%d", i)})
			}
		}
		r.Summary = fmt.Sprintf("%d deadcode candidates; capture complete=%t; graph diagnostics=%d", len(v.Nodes), v.Snapshot.Complete, len(v.Diagnostics))
		r.analysisDiagnostics(v.Snapshot.Diagnostics, "snapshot", "/data/snapshot/diagnostics")
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "graph", Message: d.Message, Path: d.Location.Path, Line: d.Location.Line, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case repokit.ValidationReport:
		describeValidation(r, v)
	case repokit.ValidationPlan:
		r.Summary = fmt.Sprintf("Planned checks for %d Components; no checks executed", len(v.Components))
		for i, group := range v.Components {
			for j, check := range group.Checks {
				if check.Scope == "unavailable" {
					r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "validation.check_unavailable", Severity: "warning", Phase: "planning", Subject: group.Component.Root + ":" + check.Check, Message: check.Reason, Evidence: fmt.Sprintf("/data/components/%d/checks/%d", i, j)})
				}
			}
		}
	case repokit.ValidationRunList:
		r.Summary = fmt.Sprintf("Read %d of %d retained validation runs; hasMore=%t", len(v.Runs), v.Total, v.HasMore)
	case repokit.ValidationRunDetail:
		r.Summary = fmt.Sprintf("Historical run %s: %s; evidence complete=%t", v.Run.RunID, v.Run.Status, v.Complete)
		if !v.Complete {
			r.Outcome = "partial"
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "validation.evidence_unavailable", Severity: "warning", Phase: "history", Subject: d.Artifact, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case repokit.ValidationHistory:
		r.Summary = "Read retained validation history"
	case worktree.EnsureReport:
		describeResult(r, v.EnsureWorktreeReport)
		if v.Preparation != nil {
			r.Summary += fmt.Sprintf("; dependencies ready=%t", v.Ready)
			if !v.Ready {
				r.Outcome = "partial"
			}
			r.Error = v.PreparationError
			for _, p := range v.Preparation.Phases {
				name := "depot." + p.Name
				if p.Bin != "" {
					name += "/" + p.Bin
				}
				r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: name, DurationSeconds: p.DurationSeconds})
			}
			for i, bin := range v.Preparation.Bins {
				if bin.Status != "ready" {
					message := bin.Reason
					if bin.Error != nil {
						message = bin.Error.Message
					}
					if message == "" {
						message = "dependency status: " + bin.Status
					}
					r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "depot.not_ready", Severity: "warning", Phase: "depot", Path: bin.Path, Message: message, Evidence: fmt.Sprintf("/data/preparation/bins/%d", i)})
				}
			}
		}
	case state.EnsureWorktreeReport:
		r.Summary = fmt.Sprintf("Worktree %s %s; confirmed=%t", v.Action, v.Checkout, v.Confirmed)
		for _, p := range v.Phases {
			r.Cost.Phases = append(r.Cost.Phases, phaseCost{Name: "worktree." + p.Name, DurationSeconds: p.DurationSeconds})
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: "worktree", Path: v.Checkout, Message: d.Message, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	case state.CleanReport:
		r.Summary = fmt.Sprintf("%d cleanup actions; %d estimated bytes; %d errors", len(v.Actions), v.EstimatedBytes, len(v.Errors))
		if len(v.Errors) > 0 {
			r.Outcome = "partial"
		}
		for i, e := range v.Errors {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "state.cleanup_incomplete", Severity: "error", Phase: "cleanup", Message: e, Evidence: fmt.Sprintf("/data/errors/%d", i)})
		}
	case state.WorktreeCleanReport:
		r.Summary = fmt.Sprintf("%d maintenance actions; %d worktrees remaining; %d errors", len(v.Actions), v.RemainingCount, len(v.Errors))
		r.maintenanceDiagnostics(v, "error", "/data")
		if len(v.Errors) > 0 {
			r.Outcome = "partial"
		}
	case []discussion.Discussion:
		r.Summary = fmt.Sprintf("%d discussion(s)", len(v))
	case discussion.Discussion:
		r.Summary = fmt.Sprintf("Thread %s %s; %d comment(s)", v.ID, v.Resolution, len(v.Comments))
	case discussion.CommentResult:
		r.Summary = fmt.Sprintf("Comment %s posted in thread %s", v.Comment.ID, v.ThreadID)
	case forge.OperationResult:
		r.Summary = fmt.Sprintf("PR #%d %s; created=%t", v.PullRequest.Key.Number, v.PullRequest.State, v.Created)
	case forge.PullRequest:
		r.Summary = fmt.Sprintf("PR #%d %s", v.Key.Number, v.State)
	case []forge.PullRequest:
		// A successful empty list is known empty, never null/unknown.
		if v == nil {
			r.Data = []forge.PullRequest{}
		}
		r.Summary = fmt.Sprintf("%d pull requests", len(v))
	case stats.Report:
		r.Summary = fmt.Sprintf("Retained statistics; complete=%t; %d diagnostics", v.Complete, len(v.Diagnostics))
		if !v.Complete {
			r.Outcome = "partial"
		}
		for i, d := range v.Diagnostics {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "stats.incomplete", Severity: "error", Phase: "stats", Message: d, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
		}
	}
}

func (r *commandResult) analysisDiagnostics(ds []repokit.Diagnostic, phase, path string) {
	for i, d := range ds {
		r.Diagnostics = append(r.Diagnostics, diagnostic{Code: d.Code, Severity: "warning", Phase: phase, Message: d.Message, Path: d.Path, Line: d.Line, Version: d.Version, Evidence: fmt.Sprintf("%s/%d", path, i)})
	}
}

func (r *commandResult) maintenanceDiagnostics(v state.WorktreeCleanReport, severity, path string) {
	for i, d := range v.Errors {
		r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "worktree.maintenance_incomplete", Severity: severity, Phase: "maintenance", Message: d, Evidence: fmt.Sprintf("%s/errors/%d", path, i)})
	}
	for i, a := range v.Actions {
		if a.Status == "failed" {
			r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "worktree.action_failed", Severity: severity, Phase: "maintenance", Subject: a.Path, Message: a.Reason, Evidence: fmt.Sprintf("%s/actions/%d", path, i)})
		}
	}
}
