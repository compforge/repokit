package cli

import (
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/stats"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/state"
)

// describeResult projects public domain facts; it never infers failure from log text.
// Analysis completeness remains distinct from successful command execution.
func describeResult(r *commandResult, data any) {
	r.Outcome = "succeeded"
	switch v := data.(type) {
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
	case repokit.ValidationHistory:
		r.Summary = "Read retained validation history"
	case state.WorktreeCleanReport:
		r.Summary = fmt.Sprintf("%d maintenance actions; %d worktrees remaining; %d errors", len(v.Actions), v.RemainingCount, len(v.Errors))
		r.maintenanceDiagnostics(v, "error", "/data")
		if len(v.Errors) > 0 {
			r.Outcome = "partial"
		}
	case forge.OperationResult:
		r.Summary = fmt.Sprintf("PR #%d %s; created=%t; %d maintenance errors", v.PullRequest.Key.Number, v.PullRequest.State, v.Created, len(v.Maintenance.Errors))
		// +spec=Maintenance warnings cannot turn a confirmed PR write into a failed write
		r.maintenanceDiagnostics(v.Maintenance, "warning", "/data/maintenance")
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
