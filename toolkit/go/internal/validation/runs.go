package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/validation/history"
	"github.com/compforge/repokit/toolkit/go/state"
)

// ValidationRunQuery selects retained actual executions, never current validation evidence.
// Repository identifies the store through a live checkout. Checkout filters historical paths,
// including removed worktrees; an empty filter includes all checkouts in that repository.
type ValidationRunQuery struct {
	HistoryDir    string
	Repository    string
	Checkout      string
	Component     string
	Check         string
	Status        string
	Limit         int // zero defaults to 20; maximum 100.
	Offset        int
	IncludeInputs bool // load input summaries for this page, without reading outputs.
}

// ValidationRun is one actual check execution. Reuse does not create or reorder runs.
type ValidationRun struct {
	RunID            string                     `json:"runID"`
	Checkout         string                     `json:"checkout"`
	Component        string                     `json:"component"`
	Check            string                     `json:"check"`
	Scope            string                     `json:"scope"`
	Status           string                     `json:"status"`
	StartedAt        time.Time                  `json:"startedAt"`
	FinishedAt       *time.Time                 `json:"finishedAt,omitempty"`
	DurationSeconds  float64                    `json:"durationSeconds"`
	InputsConsistent *bool                      `json:"inputsConsistent"`
	InputID          string                     `json:"inputID"`
	ReuseCount       int                        `json:"reuseCount"`
	LastReusedAt     *time.Time                 `json:"lastReusedAt,omitempty"`
	Inputs           *ValidationRunInputSummary `json:"inputs,omitempty"`
}

// ValidationRunInputSummary describes input evidence only, never output availability or reuse eligibility.
type ValidationRunInputSummary struct {
	Snapshot    string                    `json:"snapshot,omitempty"`
	Complete    bool                      `json:"complete"`
	Diagnostics []ValidationEvidenceIssue `json:"diagnostics"`
}

type ValidationRunList struct {
	Repository string          `json:"repository"`
	Runs       []ValidationRun `json:"runs"`
	Total      int             `json:"total"`
	Offset     int             `json:"offset"`
	Limit      int             `json:"limit"`
	HasMore    bool            `json:"hasMore"`
}

// ValidationInputFile contains a content digest, never the source or environment values.
type ValidationInputFile = inputEntry
type ValidationRunInputs = inputManifest
type ValidationEvidenceIssue = history.EvidenceIssue

type ValidationRunDetail struct {
	Repository  string                    `json:"repository"`
	Run         ValidationRun             `json:"run"`
	Result      CheckResult               `json:"result"`
	Inputs      *ValidationRunInputs      `json:"inputs"`
	Complete    bool                      `json:"complete"` // artifact availability, not check success or present-day applicability.
	Diagnostics []ValidationEvidenceIssue `json:"diagnostics"`
}

func runStore(ctx context.Context, directory, checkout string) (*history.Store, error) {
	if directory == "" {
		home, err := state.ResolveHome("")
		if err != nil {
			return nil, err
		}
		directory = filepath.Join(home, "repositories")
	}
	return history.Open(ctx, directory, checkout)
}

func runResult(run *history.Run) (CheckResult, error) {
	payload := run.Result
	if payload == nil {
		payload = run.Start.Check
	}
	var result CheckResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, err
	}
	if result.RunID != run.Start.Run || result.Check != run.Check {
		return result, errors.New("history check identity does not match its Run")
	}
	if run.Result == nil {
		result.Status = "unfinished"
	}
	return result, nil
}

func runSummary(run *history.Run, result CheckResult) ValidationRun {
	r := ValidationRun{RunID: run.Start.Run, Checkout: run.Start.Checkout, Component: run.Start.Component,
		Check: run.Check, Scope: result.Scope, Status: result.Status, StartedAt: run.Start.At,
		DurationSeconds: result.DurationSeconds, InputID: run.Start.Inputs, ReuseCount: run.ReuseCount}
	if run.Result != nil {
		r.FinishedAt = &run.FinishedAt
		r.InputsConsistent = &run.Consistent
	}
	if !run.LastReusedAt.IsZero() {
		r.LastReusedAt = &run.LastReusedAt
	}
	return r
}

// +spec=History filters and pagination never run checks, renew access, prune records or infer current-code validity.
// +spec=IncludeInputs reads only the selected page manifests under the replay lock, never output logs.
// +link=component://docs/validation.md
func ListValidationRuns(ctx context.Context, q ValidationRunQuery) (ValidationRunList, error) {
	report := ValidationRunList{Runs: []ValidationRun{}, Offset: q.Offset, Limit: q.Limit}
	if report.Limit == 0 {
		report.Limit = 20
	}
	if report.Limit < 1 || report.Limit > 100 || q.Offset < 0 {
		return report, &errs.Error{Code: "validation.invalid_query", Message: "limit must be 1–100 and offset must be nonnegative"}
	}
	if q.Check != "" && q.Check != "lint" && q.Check != "test" {
		return report, &errs.Error{Code: "validation.invalid_query", Message: "check must be lint or test"}
	}
	if q.Status != "" && !slices.Contains([]string{"passed", "failed", "cancelled", "uncertain", "unfinished"}, q.Status) {
		return report, &errs.Error{Code: "validation.invalid_query", Message: "invalid run status"}
	}
	if q.Checkout != "" {
		var err error
		q.Checkout, err = filepath.Abs(q.Checkout)
		if err != nil {
			return report, err
		}
		// Removed checkouts remain queryable; only resolve symlinks when they still exist.
		canonical, err := filepath.EvalSymlinks(q.Checkout)
		if err == nil {
			q.Checkout = canonical
		} else if !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
	}
	store, err := runStore(ctx, q.HistoryDir, q.Repository)
	if err != nil {
		return report, err
	}
	err = store.Inspect(ctx, func(state *history.State) error {
		report.Repository = state.Summary.Repository
		for _, run := range state.Runs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if (q.Checkout != "" && run.Start.Checkout != q.Checkout) || (q.Component != "" && run.Start.Component != q.Component) || (q.Check != "" && run.Check != q.Check) {
				continue
			}
			result, err := runResult(run)
			if err != nil {
				return err
			}
			if q.Status != "" && result.Status != q.Status {
				continue
			}
			report.Runs = append(report.Runs, runSummary(run, result))
		}
		slices.SortFunc(report.Runs, func(a, b ValidationRun) int {
			if n := b.StartedAt.Compare(a.StartedAt); n != 0 {
				return n
			}
			return strings.Compare(a.RunID, b.RunID)
		})
		report.Total = len(report.Runs)
		start := min(q.Offset, report.Total)
		end := start + min(report.Limit, report.Total-start)
		report.Runs, report.HasMore = report.Runs[start:end], end < report.Total
		if q.IncludeInputs {
			for i := range report.Runs {
				if err := ctx.Err(); err != nil {
					return err
				}
				row := &report.Runs[i]
				evidence := &ValidationRunInputSummary{Diagnostics: []ValidationEvidenceIssue{}}
				data, err := store.LoadInputs(row.InputID)
				if err != nil {
					evidence.Diagnostics = append(evidence.Diagnostics, ValidationEvidenceIssue{Artifact: "inputs", Message: err.Error()})
				} else {
					inputs, issue := decodeRunInputs(data, row.Checkout)
					if issue != nil {
						evidence.Diagnostics = append(evidence.Diagnostics, *issue)
					} else {
						evidence.Snapshot, evidence.Complete = inputs.Snapshot, true
					}
				}
				row.Inputs = evidence
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return ValidationRunList{}, err
	}
	slog.DebugContext(ctx, "validation history listed", "repository", report.Repository, "matched", report.Total, "returned", len(report.Runs))
	return report, nil
}

func ReadValidationRun(ctx context.Context, directory, checkout, id string) (ValidationRunDetail, error) {
	report := ValidationRunDetail{Diagnostics: []ValidationEvidenceIssue{}}
	if !runIDPattern.MatchString(id) {
		return report, &errs.Error{Code: "validation.invalid_run_id", Message: "invalid run ID"}
	}
	store, err := runStore(ctx, directory, checkout)
	if err != nil {
		return report, err
	}
	evidence, err := store.ReadRun(ctx, id)
	if err != nil {
		if errors.Is(err, history.ErrRunNotFound) {
			return report, &errs.Error{Code: "validation.run_not_found", Message: "run is not present in retained repository history", Subject: id, Cause: err}
		}
		return report, err
	}
	result, err := runResult(evidence.Run)
	if err != nil {
		return report, err
	}
	result.Stdout, result.Stderr = evidence.Stdout, evidence.Stderr
	report.Repository, report.Run, report.Result = evidence.Repository, runSummary(evidence.Run, result), result
	report.Diagnostics = evidence.Issues
	if evidence.Inputs != nil {
		inputs, issue := decodeRunInputs(evidence.Inputs, report.Run.Checkout)
		if issue != nil {
			report.Diagnostics = append([]ValidationEvidenceIssue{*issue}, report.Diagnostics...)
		} else {
			report.Inputs = inputs
		}
	}

	if err := ctx.Err(); err != nil {
		return ValidationRunDetail{}, err
	}
	report.Complete = len(report.Diagnostics) == 0
	slog.DebugContext(ctx, "validation run read", "repository", report.Repository, "run", id, "status", report.Run.Status, "complete", report.Complete)
	return report, nil
}

var runIDPattern = regexp.MustCompile(`^[A-Z2-7]+$`)

func decodeRunInputs(data []byte, checkout string) (*ValidationRunInputs, *ValidationEvidenceIssue) {
	var inputs ValidationRunInputs
	err := json.Unmarshal(data, &inputs)
	if err == nil && (inputs.SchemaVersion != 1 || inputs.Checkout != checkout || inputs.Files == nil || inputs.Snapshot == "") {
		err = errors.New("input manifest identity or schema is invalid")
	}
	if err != nil {
		return nil, &ValidationEvidenceIssue{Artifact: "inputs", Message: fmt.Sprintf("decode input evidence: %v", err)}
	}
	return &inputs, nil
}
