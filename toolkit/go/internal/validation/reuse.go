package validation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/validation/history"
)

// HistoryCounts counts real attempts separately from reuse.
type HistoryCounts = history.Counts

// ValidationHistory describes retained repository records, not lifetime totals.
type ValidationHistory = history.Summary

// ValidationHistories groups repository statistics with per-store read diagnostics.
type ValidationHistories = history.Summaries

func ReadValidationHistories(ctx context.Context, directory string) (ValidationHistories, error) {
	return history.ReadSummaries(ctx, directory)
}

// ReadValidationHistory reads retained execution statistics without executing checks.
func ReadValidationHistory(ctx context.Context, directory, checkout string) (ValidationHistory, error) {
	return history.ReadSummary(ctx, directory, checkout)
}

type historySession struct {
	mu          sync.Mutex
	store       *history.Store
	state       *history.State
	manifests   map[string]inputManifest
	startedAt   map[string]time.Time
	diagnostics []string
	failed      bool
	inputID     string
	manifest    inputManifest
	force       bool
}

func beginHistory(ctx context.Context, plan ValidationPlan) *historySession {
	s := &historySession{manifests: map[string]inputManifest{}, startedAt: map[string]time.Time{}, inputID: plan.InputID, manifest: plan.manifest, force: plan.force}
	if plan.historyDir == "" {
		return s
	}
	var err error
	s.store, err = history.Open(ctx, plan.historyDir, plan.Checkout)
	if err != nil {
		s.fail(ctx, err)
		return s
	}
	state, err := s.store.Maintain(ctx, plan.retentionDays)
	if err == nil {
		var data []byte
		data, err = json.Marshal(plan.manifest)
		if err == nil {
			err = s.store.SaveInputs(plan.InputID, data)
		}
	}
	if err != nil {
		s.fail(ctx, err)
		return s
	}
	s.state = state
	return s
}

func (s *historySession) fail(ctx context.Context, err error) {
	s.failed = true
	message := "validation history unavailable: " + err.Error()
	s.diagnostics = append(s.diagnostics, message)
	slog.WarnContext(ctx, "validation history unavailable", "error", err)
}

// candidate keeps searching older runs after a mismatch; the latest same-task
// candidate explains a miss only when no exact match exists.
func (s *historySession) candidate(root string, spec CheckSpec) (*history.Run, *CheckResult, string, error) {
	var reason string
	for _, id := range slices.Backward(s.state.Order) {
		run := s.state.Runs[id]
		if run.Start.Checkout != s.manifest.Checkout || run.Start.Component != root || run.Check != spec.Check {
			continue
		}
		var result CheckResult
		if err := json.Unmarshal(run.Result, &result); err != nil {
			return nil, nil, "", err
		}
		var mismatch string
		switch {
		case result.Scope != spec.Scope || !slices.Equal(result.Command, spec.Command) || !slices.Equal(result.Files, spec.Files):
			mismatch = "check command, scope or files changed"
		case !slices.Equal(result.Inputs.Components, spec.Inputs.Components) || !slices.Equal(result.Inputs.Paths, spec.Inputs.Paths):
			mismatch = "check input declaration changed"
		case !reflect.DeepEqual(result.DepotWitnesses, spec.DepotWitnesses) || !reflect.DeepEqual(result.Witnesses, spec.Witnesses):
			mismatch = "dependency evidence changed"
		case result.Environment != spec.Environment:
			mismatch = "execution environment changed"
		default:
			manifest, ok := s.manifests[run.Start.Inputs]
			if !ok {
				var err error
				manifest, err = s.loadInputs(run.Start.Inputs)
				if err != nil {
					return nil, nil, "", err
				}
				s.manifests[run.Start.Inputs] = manifest
			}
			if sameInputs(manifest, result.InputFiles, s.manifest, spec.InputFiles) {
				return run, &result, "", nil
			}
			mismatch = "check input contents changed"
		}
		if reason == "" {
			reason = mismatch
		}
	}
	if reason == "" {
		reason = "no matching completed check"
	}
	return nil, nil, reason, nil
}

// +spec=Only the latest matching completed Run may grant reuse; unrelated runs never revoke its evidence
// +link=component://docs/validation.md
// +spec=Successful history grants reuse only for the same task, inputs and environment
// +spec=History never chooses or widens affected-file execution scope
func (s *historySession) reuse(ctx context.Context, root string, spec CheckSpec) (*CheckResult, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.force {
		return nil, "forced execution"
	}
	if !depotReusable(spec.DepotWitnesses) {
		return nil, "dependency Bin evidence unavailable"
	}
	if spec.Inputs.DisableReuse {
		return nil, "reuse disabled by caller"
	}
	if s.failed {
		return nil, "history unavailable"
	}
	if s.store == nil {
		return nil, "history disabled"
	}
	run, previous, reason, err := s.candidate(root, spec)
	if err != nil {
		s.fail(ctx, err)
		return nil, "history result unavailable"
	}
	if run == nil {
		return nil, reason
	}
	if run.Status != "passed" {
		return nil, "latest matching check did not pass"
	}
	if !run.Consistent || run.CheckedAt.IsZero() || previous.Inputs.DisableReuse {
		return nil, "latest matching check has no reusable evidence"
	}
	if previous.Stdout, previous.Stderr, err = s.store.LoadOutput(ctx, previous.RunID, run.Start.At); err != nil {
		s.fail(ctx, err)
		return nil, "history output unavailable"
	}
	return previous, "matching successful run inputs, command, scope and environment"
}

func historyRecord(kind, checkout, root, inputs string, result CheckResult, consistent bool) history.Event {
	result.Stdout, result.Stderr = "", ""
	data, _ := json.Marshal(result) // CheckResult contains only JSON-safe fields.
	return history.Event{SchemaVersion: 2, Checkout: checkout, Type: kind, At: time.Now().UTC(), Run: result.RunID, Component: root, Inputs: inputs, Check: data, Consistent: consistent}
}

func (s *historySession) started(ctx context.Context, root string, spec CheckSpec) string {
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil && !s.failed {
		e := historyRecord("started", s.manifest.Checkout, root, s.inputID, CheckResult{CheckSpec: spec, RunID: id}, false)
		s.startedAt[id] = e.At
		if err := s.store.Append(ctx, []history.Event{e}); err != nil {
			s.fail(ctx, err)
		}
	}
	return id
}

func (s *historySession) finish(ctx context.Context, report *ValidationReport) {
	if s.store == nil {
		report.Diagnostics = append(report.Diagnostics, s.diagnostics...)
		return
	}
	if !s.failed {
		events := []history.Event{}
		for _, group := range report.Components {
			for _, result := range group.Checks {
				if result.RunID == "" {
					continue
				}
				kind := "reused"
				if !result.Reused {
					kind = "finished"
					if err := s.store.SaveOutput(ctx, result.RunID, s.startedAt[result.RunID], result.Stdout, result.Stderr); err != nil {
						s.fail(ctx, err)
						break
					}
				}
				events = append(events, historyRecord(kind, report.Checkout, group.Component.Root, s.inputID, result, report.InputsConsistent))
			}
			if s.failed {
				break
			}
		}
		if !s.failed {
			if err := s.store.Append(ctx, events); err != nil {
				s.fail(ctx, err)
			}
		}
	}
	if !s.failed {
		state, err := s.store.Read(ctx)
		if err != nil {
			s.fail(ctx, err)
		} else {
			report.History = &state.Summary
		}
	}
	report.Diagnostics = append(report.Diagnostics, s.diagnostics...)
}

func (s *historySession) loadInputs(id string) (inputManifest, error) {
	var manifest inputManifest
	data, err := s.store.LoadInputs(id)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	if manifest.SchemaVersion != 1 || manifest.Checkout != s.manifest.Checkout || manifest.Files == nil {
		return manifest, errors.New("invalid input record")
	}
	return manifest, nil
}
