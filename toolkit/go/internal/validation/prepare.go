package validation

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/git"
)

// Format executes Makefile format targets to normalize source formatting. Changes never
// become lint/test evidence. Validate replans checks after this explicit stage.
// +spec=Format runs before lint/test and never grants reusable validation evidence
// +spec=Format targets normalize source formatting; lint auto-fixes are outside this contract
func Format(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	report, _, err := formatValidation(ctx, req)
	return report, err
}

func formatValidation(ctx context.Context, req ValidationRequest) (report ValidationReport, changed []string, err error) {
	phases := phaseRecorder{ctx: ctx}
	defer func() {
		phases.finish()
		report.Phases = phases.phases
	}()
	req.Checks = []string{"format"}
	req.Inputs, req.HistoryDir = nil, ""
	plan, err := PlanValidation(ctx, req)
	for _, phase := range plan.Phases {
		phase.Name = "format." + phase.Name
		phases.phases = append(phases.phases, phase)
	}
	if err != nil {
		return ValidationReport{}, nil, err
	}
	report = ValidationReport{SchemaVersion: 2, Checkout: plan.Checkout, Snapshot: plan.Snapshot, Components: []ComponentValidationResult{}, Preparation: []ComponentValidationResult{}, Diagnostics: []string{}, Complete: true, Passed: true}
	phases.start("format.inputs.before")
	before, err := analysis.Capture(ctx, analysis.InputRequest{Repository: plan.Checkout})
	if err != nil {
		return report, nil, err
	}
	if !before.Report.Complete || before.Report.Snapshot != plan.Snapshot {
		return report, nil, errors.New("contents changed before format")
	}
	phases.start("format.checks")
	for _, group := range plan.Components {
		spec := group.Checks[0]
		result := CheckResult{CheckSpec: spec, Status: spec.Scope, CheckedAt: time.Now().UTC()}
		if spec.Scope != "skipped" && spec.Scope != "unavailable" {
			started := time.Now()
			result = executeMake(ctx, filepath.Join(plan.Checkout, group.Component.Root), spec, plan.environment)
			result.CheckedAt, result.DurationSeconds = time.Now().UTC(), time.Since(started).Seconds()
		}
		report.Preparation = append(report.Preparation, ComponentValidationResult{Component: group.Component, Checks: []CheckResult{result}})
		if result.Status != "passed" && result.Status != "skipped" {
			report.Passed = false
		}
		if result.Status != "passed" && result.Status != "failed" && result.Status != "skipped" && result.Status != "unavailable" {
			report.Complete = false
			break
		}
	}
	phases.start("format.inputs.after")
	after, err := analysis.Capture(ctx, analysis.InputRequest{Repository: plan.Checkout})
	if err != nil {
		return report, nil, err
	}
	if !after.Report.Complete {
		report.Complete, report.Passed = false, false
		report.Diagnostics = append(report.Diagnostics, "final content capture after format is incomplete")
	}
	report.Snapshot = after.Report.Snapshot
	// A format report describes resulting contents, not validation consistency.
	return report, changedValidationPaths(before.Contents, after.Contents), nil
}

// Compare already-captured content, without another repository scan. Format-created
// inputs join the original diff seeds so generated Components are not silently omitted.
func changedValidationPaths(before, after git.Snapshot) []string {
	changed := map[string]bool{}
	for path, value := range before.Files {
		if next, ok := after.Files[path]; !ok || !bytes.Equal(value, next) {
			changed[path] = true
		}
	}
	for path := range after.Files {
		if _, ok := before.Files[path]; !ok {
			changed[path] = true
		}
	}
	for _, pair := range [][2]map[string]string{{before.Links, after.Links}, {before.Modules, after.Modules}, {before.Opaque, after.Opaque}} {
		for path, value := range pair[0] {
			if next, ok := pair[1][path]; !ok || value != next {
				changed[path] = true
			}
		}
		for path := range pair[1] {
			if _, ok := pair[0][path]; !ok {
				changed[path] = true
			}
		}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}
