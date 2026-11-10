package validation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/gitenv"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// ValidationRequest selects Makefile-defined checks. Files are repository-relative
// candidates, not shell arguments. Nil components means all discovered Components;
// an explicitly empty slice means none. An impact report is supplied, never obtained
// implicitly by executing validation. Tags selects affected file roles for automatic checks.
type ValidationRequest struct {
	Base                 string
	ChangedFiles         []string
	Repository           string
	Components           []string
	Checks               []string
	Files                []string
	Impact               *analysis.Report
	Tags                 []string
	Concurrency          int // zero defaults to two concurrent checks.
	Inputs               map[string]map[string]CheckInputDeclaration
	HistoryDir           string   // empty disables persistent history; CLI supplies its default.
	Force                bool     // execute even when reusable history exists.
	HistoryRetentionDays int      // zero defaults to 30; expired event prefixes are truncated regardless of references.
	Environment          []string // nil inherits the caller environment without Git checkout overrides or tool call identity; never serialized.
}

// CheckSpec freezes one Component/check invocation. Scope is full, focused,
// skipped or unavailable. The executor does not rediscover targets or select files.
type CheckSpec struct {
	DepotWitnesses      map[string]string     `json:"depotWitnesses,omitempty"`
	Check               string                `json:"check"`
	RequestedComponents []string              `json:"requestedComponents"`
	Scope               string                `json:"scope"`
	Command             []string              `json:"command"`
	Files               []string              `json:"files"`
	Reason              string                `json:"reason"`
	InputFiles          []string              `json:"inputFiles"`
	InputReason         string                `json:"inputReason"`
	Inputs              CheckInputDeclaration `json:"inputs"`
	Witnesses           map[string]string     `json:"witnesses"`
	Environment         string                `json:"environment"` // digest only; values can contain secrets.
}

type ComponentValidationPlan struct {
	Component project.Binding `json:"component"`
	Checks    []CheckSpec     `json:"checks"`
}

// ValidationPlan is a live, content-bound plan. JSON is an observation, not an
// executable artifact: runtime environment and the integrity seal are private.
type ValidationPlan struct {
	Impact        *analysis.Report          `json:"impact,omitempty"`
	Checkout      string                    `json:"checkout"`
	Snapshot      string                    `json:"snapshot"`
	Components    []ComponentValidationPlan `json:"components"`
	Concurrency   int                       `json:"concurrency"`
	InputID       string                    `json:"inputID"`
	environment   []string
	historyDir    string
	retentionDays int
	force         bool
	manifest      inputManifest
	seal          string
}

// CheckResult records command outcome separately from round-level consistency.
// A passed command is not reusable evidence unless report inputs stayed consistent.
type CheckResult struct {
	CheckSpec
	Status          string    `json:"status"`
	CheckedAt       time.Time `json:"checkedAt"`
	RunID           string    `json:"runID"`
	RequestedAt     time.Time `json:"requestedAt"`
	LookupSeconds   float64   `json:"lookupSeconds"`
	Reused          bool      `json:"reused"`
	ReuseReason     string    `json:"reuseReason"`
	ExitCode        *int      `json:"exitCode"`
	DurationSeconds float64   `json:"durationSeconds"`
	Stdout          string    `json:"stdout"`
	Stderr          string    `json:"stderr"`
	Diagnostic      string    `json:"diagnostic,omitempty"`
}

type ComponentValidationResult struct {
	Component project.Binding `json:"component"`
	Checks    []CheckResult   `json:"checks"`
}

// ValidationReport reports execution, not permission to commit or merge.
// Complete means every selected check reached a known terminal state.
// InputsConsistent independently guards evidence; unavailable is never Passed.
type ValidationReport struct {
	InputID          string                      `json:"inputID"`
	History          *ValidationHistory          `json:"history,omitempty"`
	Impact           *analysis.Report            `json:"impact,omitempty"`
	Preparation      []ComponentValidationResult `json:"preparation,omitempty"`
	SchemaVersion    int                         `json:"schemaVersion"`
	InputsConsistent bool                        `json:"inputsConsistent"`
	Checkout         string                      `json:"checkout"`
	Snapshot         string                      `json:"snapshot"`
	Components       []ComponentValidationResult `json:"components"`
	Complete         bool                        `json:"complete"`
	Passed           bool                        `json:"passed"`
	Diagnostics      []string                    `json:"diagnostics"`
}

var makeFilePath = regexp.MustCompile(`^[A-Za-z0-9_./@+][A-Za-z0-9_./@+:-]*$`)

// PlanValidation only reads the checkout. Preparation/formatting must precede it.
// +spec=Inherited tool call identity is removed from both execution and reuse inputs; explicit environments retain it
// +why=Per-invocation tracing metadata must not invalidate otherwise identical checks
// +spec=Makefile is the execution contract; absent targets never select language-specific commands
// +spec=Missing lint/test targets fall back only to the root Component; format never falls back
// +spec=Recorded inputs bind execution evidence and never decide affected-file selection
// +link=component://docs/validation.md
func PlanValidation(ctx context.Context, req ValidationRequest) (ValidationPlan, error) {
	if req.Base != "" {
		if req.Impact != nil {
			return ValidationPlan{}, errors.New("base and impact are alternative selections")
		}
		impact, err := analysis.Analyze(ctx, analysis.Request{Repository: req.Repository, Base: req.Base, ChangedFiles: req.ChangedFiles})
		if err != nil {
			return ValidationPlan{}, err
		}
		req.Impact = &impact
	}
	captured, err := analysis.Capture(ctx, analysis.InputRequest{Repository: req.Repository})
	if err != nil {
		return ValidationPlan{}, err
	}
	observed := captured.Report
	if !observed.Complete {
		return ValidationPlan{}, errors.New("validation snapshot is incomplete")
	}
	layout, err := analysis.Inspect(ctx, analysis.InputRequest{Repository: observed.Checkout})
	if err != nil {
		return ValidationPlan{}, err
	}
	if !layout.Complete {
		return ValidationPlan{}, errors.New("validation layout is incomplete")
	}
	checks := req.Checks
	if checks == nil {
		checks = []string{"lint", "test"}
	}
	checks = slices.Clone(checks)
	sort.Strings(checks)
	checks = slices.Compact(checks)
	for _, check := range checks {
		if check != "lint" && check != "test" && check != "format" {
			return ValidationPlan{}, fmt.Errorf("unsupported check %q", check)
		}
	}
	if len(req.Files) > 0 && req.Impact != nil {
		return ValidationPlan{}, errors.New("files and impact are alternative selections")
	}
	if req.Impact != nil && (req.Impact.Snapshot != observed.Snapshot || req.Impact.Input != "working_tree" || req.Impact.Checkout != observed.Checkout) {
		return ValidationPlan{}, errors.New("impact does not describe current checkout contents")
	}
	env := req.Environment
	if env == nil {
		env = gitenv.ForCheckout(os.Environ())
	}
	// Make's inherited flags/overrides can turn a frozen full command into a dry-run,
	// another Makefile or a narrowed check. These control inputs belong to the plan.
	envMap := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, '\x00') {
			return ValidationPlan{}, errors.New("invalid execution environment")
		}
		switch key {
		case "_", "SHLVL", "PWD", "OLDPWD", "MAKEFILES", "MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "GNUMAKEFLAGS", "MAKELEVEL", "LINT_FILES", "TEST_FILES":
			continue
		}
		if req.Environment == nil && key == "CODEX_TOOL_CALL_ID" {
			continue
		}
		envMap[key] = value
	}
	env = make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	encoded, _ := json.Marshal(env)
	digest := sha256.Sum256(encoded)
	plan := ValidationPlan{Checkout: observed.Checkout, Snapshot: observed.Snapshot, Components: []ComponentValidationPlan{}, environment: env}
	plan.Impact = req.Impact
	plan.Concurrency = req.Concurrency
	if plan.Concurrency == 0 {
		plan.Concurrency = 2
	}
	if plan.Concurrency < 1 || plan.Concurrency > 64 {
		return ValidationPlan{}, errors.New("validation concurrency must be between 1 and 64")
	}
	if req.HistoryRetentionDays < 0 {
		return ValidationPlan{}, errors.New("history retention days must be non-negative")
	}
	plan.historyDir, plan.force = req.HistoryDir, req.Force
	plan.retentionDays = req.HistoryRetentionDays
	if plan.retentionDays == 0 {
		plan.retentionDays = 30
	}
	plan.InputID = rand.Text()
	plan.manifest = captureInputs(captured)
	selected := map[string]bool{}
	for _, root := range req.Components {
		selected[root] = false
	}
	tags := req.Tags
	if tags == nil {
		tags = []string{"source", "test", "config", "manifest", "lockfile"}
	}
	contracts := map[string]makeContract{}
	contractFor := func(component project.Binding) (makeContract, error) {
		if contract, ok := contracts[component.Root]; ok {
			return contract, nil
		}
		contract, err := validationMakeContract(ctx, plan.Checkout, component.Root)
		if err == nil {
			contracts[component.Root] = contract
		}
		return contract, err
	}
	var rootComponent *project.Binding
	for i := range layout.Components {
		if layout.Components[i].Root == "." {
			rootComponent = &layout.Components[i]
			break
		}
	}
	for _, component := range layout.Components {
		if req.Components != nil {
			if _, ok := selected[component.Root]; !ok {
				continue
			}
			selected[component.Root] = true
		}
		contract, err := contractFor(component)
		if err != nil {
			return ValidationPlan{}, err
		}
		for _, check := range checks {
			spec := CheckSpec{Check: check, RequestedComponents: []string{component.Root}, Scope: "full", Command: []string{}, Files: []string{}, Reason: "full Component check", Environment: hex.EncodeToString(digest[:])}
			candidates := slices.Clone(req.Files)
			if req.Impact != nil {
				var impact *project.ComponentImpact
				for i := range req.Impact.Components {
					item := &req.Impact.Components[i]
					if item.Root == component.Root && item.Snapshot == "after" {
						impact = item
						break
					}
				}
				if impact == nil {
					return ValidationPlan{}, fmt.Errorf("impact missing current Component %q", component.Root)
				}
				if impact.AffectedFileCounts == nil {
					return ValidationPlan{}, fmt.Errorf("Component %q impact is missing affectedFileCounts", component.Root)
				}
				for _, count := range impact.AffectedFileCounts {
					if count < 0 {
						return ValidationPlan{}, errors.New("impact counts must be non-negative")
					}
				}
				relevant := false
				for _, tag := range tags {
					if impact.AffectedFileCounts[cg.Tag(tag)] > 0 {
						relevant = true
					}
				}
				if !relevant {
					spec.Scope = "skipped"
					spec.Reason = "no selected validation tags"
					appendValidationCheck(&plan, component, spec)
					continue
				}
				candidates = impact.AffectedFiles
				if check == "test" {
					candidates = impact.TestFiles
				}
				spec.Reason = "affected Component; canonical full check"
			}
			executor, executionContract := component, contract
			target := executionContract.target(check)
			missingReason := "Makefile does not declare a supported " + check + " target"
			if target == "" && check != "format" && component.Root != "." && rootComponent != nil && contract.problem == "" {
				rootContract, err := contractFor(*rootComponent)
				if err != nil {
					return ValidationPlan{}, err
				}
				if rootTarget := rootContract.target(check); rootTarget != "" {
					executor, executionContract, target = *rootComponent, rootContract, rootTarget
				} else {
					missingReason = "Component and root Makefiles do not declare a supported " + check + " target"
					if rootContract.problem != "" {
						missingReason = "root " + rootContract.problem
					}
				}
			}
			if target == "" {
				spec.Scope = "unavailable"
				spec.Reason = missingReason
				if contract.problem != "" {
					spec.Reason = contract.problem
				}
				appendValidationCheck(&plan, component, spec)
				continue
			}
			variable := strings.ToUpper(check) + "_FILES"
			if check == "format" {
				variable = "LINT_FILES"
			}
			files, reason := validationFiles(plan.Checkout, executor.Root, component.Root, layout.Layout, candidates)
			if len(candidates) > 0 {
				if reason != "" {
					spec.Reason = reason + "; canonical full check"
				} else if !executionContract.variables[variable] {
					spec.Reason = variable + " contract missing; canonical full check"
				} else {
					spec.Scope = "focused"
					spec.Files = files
					spec.Reason = "selected files under Makefile " + variable + " contract"
				}
			}
			spec.Command = []string{"make", "-f", "Makefile", target, "LINT_FILES=", "TEST_FILES="}
			if spec.Scope == "focused" {
				idx := 4
				if check == "test" {
					idx = 5
				}
				spec.Command[idx] = variable + "=" + strings.Join(spec.Files, " ")
			}
			if executor.Root != component.Root {
				spec.Reason = "fallback to root; " + spec.Reason
			}
			appendValidationCheck(&plan, executor, spec)
		}
	}
	for root, found := range selected {
		if !found {
			return ValidationPlan{}, fmt.Errorf("unknown Component %q", root)
		}
	}
	sort.Slice(plan.Components, func(i, j int) bool { return plan.Components[i].Component.Root < plan.Components[j].Component.Root })
	for i := range plan.Components {
		group := &plan.Components[i]
		sort.Slice(group.Checks, func(i, j int) bool { return group.Checks[i].Check < group.Checks[j].Check })
		for j := range group.Checks {
			spec := &group.Checks[j]
			declaration, err := validationCheckInputs(captured, layout.Layout, group.Component.Root, *spec, req.Inputs)
			if err != nil {
				return ValidationPlan{}, err
			}
			spec.Inputs = declaration
			spec.Witnesses, err = validateDeclaration(plan.Checkout, layout.Layout, group.Component.Root, declaration)
			spec.InputFiles, spec.InputReason = selectInputs(plan.manifest, layout.Layout, group.Component.Root, declaration)
			if err != nil {
				return ValidationPlan{}, fmt.Errorf("%s %s inputs: %w", group.Component.Root, spec.Check, err)
			}
			if err := attachDepotInputs(plan.Checkout, layout.Layout, group.Component.Root, spec, plan.manifest); err != nil {
				return ValidationPlan{}, err
			}
		}
	}
	plan.seal = planSeal(plan)
	return plan, nil
}

func validationFiles(root, component, source string, layout project.Layout, candidates []string) ([]string, string) {
	files := []string{}
	for _, path := range candidates {
		if filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") {
			return nil, "selected path is not normalized and repository-relative"
		}
		owner := layout.Owner(path)
		if owner == nil || owner.Root != source {
			return nil, "selected path no longer belongs to Component"
		}
		absolute := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return nil, "selected path is missing or not a regular file"
		}
		real, err := filepath.EvalSymlinks(absolute)
		if err != nil || real != absolute {
			return nil, "selected path crosses a symlink"
		}
		relative, err := filepath.Rel(filepath.Join(root, component), absolute)
		if err != nil || !makeFilePath.MatchString(relative) {
			return nil, "selected path cannot be represented by Make file-list contract"
		}
		files = append(files, filepath.ToSlash(relative))
	}
	sort.Strings(files)
	return slices.Compact(files), ""
}

func planSeal(plan ValidationPlan) string {
	encoded, _ := json.Marshal(plan)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// ExecuteValidation consumes only the frozen plan. Two round-level captures,
// independent of the number of Components/checks, guard all returned evidence.
// +spec=Planning and final consistency failures never publish a complete validation report
// +spec=The executor never recalculates the scope of a frozen command
// +link=component://docs/validation.md
func ExecuteValidation(ctx context.Context, plan ValidationPlan) (ValidationReport, error) {
	report := ValidationReport{Impact: plan.Impact, SchemaVersion: 2, InputID: plan.InputID, Checkout: plan.Checkout, Snapshot: plan.Snapshot, Components: []ComponentValidationResult{}, Diagnostics: []string{}}
	if plan.seal == "" || plan.seal != planSeal(plan) {
		return report, errors.New("validation plan was modified or deserialized; plan again")
	}
	for _, group := range plan.Components {
		for _, spec := range group.Checks {
			if spec.Check == "format" {
				return report, errors.New("format plans must run through Format or Validate before validation planning")
			}
		}
	}
	before, err := analysis.CaptureSnapshot(ctx, analysis.InputRequest{Repository: plan.Checkout})
	if err != nil {
		return report, err
	}
	if !before.Complete || before.Snapshot != plan.Snapshot || !validationWitnessesMatch(plan) {
		report.Diagnostics = append(report.Diagnostics, "inputs changed after planning; no checks executed")
		return report, nil
	}
	history := beginHistory(ctx, plan)
	type job struct{ group, check int }
	jobs := make(chan job)
	for _, component := range plan.Components {
		report.Components = append(report.Components, ComponentValidationResult{Component: component.Component, Checks: make([]CheckResult, len(component.Checks))})
	}
	var workers sync.WaitGroup
	for range plan.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				component := plan.Components[item.group]
				spec := component.Checks[item.check]
				result := CheckResult{CheckSpec: spec, Status: spec.Scope, CheckedAt: time.Now().UTC()}
				if spec.Scope != "skipped" && spec.Scope != "unavailable" {
					requested := time.Now().UTC()
					lookup := time.Now()
					previous, reason := history.reuse(ctx, component.Component.Root, spec)
					lookupSeconds := time.Since(lookup).Seconds()
					if previous != nil {
						result = *previous
						result.CheckSpec = spec
						result.Reused = true
						result.ReuseReason = reason
						slog.InfoContext(ctx, "validation check reused", "component", component.Component.Root, "check", spec.Check, "run", result.RunID)
					} else {
						slog.DebugContext(ctx, "validation check reuse missed", "checkout", plan.Checkout, "component", component.Component.Root, "check", spec.Check, "reason", reason)
						started := time.Now()
						result = executeMakeStarted(ctx, filepath.Join(plan.Checkout, component.Component.Root), spec, plan.environment, func() string { return history.started(ctx, component.Component.Root, spec) })
						result.DurationSeconds = time.Since(started).Seconds()
						result.CheckedAt = time.Now().UTC()
						result.ReuseReason = reason
					}
					result.RequestedAt, result.LookupSeconds = requested, lookupSeconds
				}
				report.Components[item.group].Checks[item.check] = result
			}
		}()
	}
	for i, group := range plan.Components {
		for j := range group.Checks {
			jobs <- job{i, j}
		}
	}
	close(jobs)
	workers.Wait()
	report.Complete, report.Passed = true, true
	for _, group := range report.Components {
		for _, result := range group.Checks {
			if result.Status != "passed" && result.Status != "skipped" {
				report.Passed = false
			}
			if result.Status != "passed" && result.Status != "failed" && result.Status != "skipped" && result.Status != "unavailable" {
				report.Complete = false
			}
		}
	}
	after, err := analysis.CaptureSnapshot(ctx, analysis.InputRequest{Repository: plan.Checkout})
	report.InputsConsistent = err == nil && after.Complete && after.Snapshot == plan.Snapshot && validationWitnessesMatch(plan)
	if !report.InputsConsistent {
		report.Passed = false
		message := "inputs changed during validation"
		if err != nil {
			message = "final content capture failed: " + err.Error()
		}
		report.Diagnostics = append(report.Diagnostics, message)
	}
	// Preserve terminal records even when execution exhausted its deadline.
	historyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	history.finish(historyCtx, &report)
	return report, nil
}

// Validate plans and executes lint/test. Lint and Test use the same planner and executor.
func Validate(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	for _, check := range req.Checks {
		if check != "format" && check != "lint" && check != "test" {
			return ValidationReport{}, fmt.Errorf("unsupported check %q", check)
		}
	}
	var preparation []ComponentValidationResult
	if req.Impact != nil && slices.Contains(req.Checks, "format") {
		return ValidationReport{}, errors.New("combined format requires Base or full selection; supplied impact cannot be refreshed after edits")
	}
	if slices.Contains(req.Checks, "format") {
		formatted, changed, err := formatValidation(ctx, req)
		if err != nil {
			return formatted, err
		}
		preparation = formatted.Preparation
		if len(req.ChangedFiles) > 0 {
			req.ChangedFiles = append(slices.Clone(req.ChangedFiles), changed...)
		}
		if !formatted.Complete {
			return formatted, nil
		}
		for _, group := range preparation {
			for _, check := range group.Checks {
				if check.Status == "failed" {
					return formatted, nil
				}
			}
		}
		req.Checks = slices.DeleteFunc(slices.Clone(req.Checks), func(check string) bool { return check == "format" })
		if len(req.Checks) == 0 {
			return formatted, nil
		}
	}
	plan, err := PlanValidation(ctx, req)
	if err != nil {
		return ValidationReport{}, err
	}
	report, err := ExecuteValidation(ctx, plan)
	report.Preparation = preparation
	return report, err
}
func Lint(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	req.Checks = []string{"lint"}
	return Validate(ctx, req)
}
func Test(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	req.Checks = []string{"test"}
	return Validate(ctx, req)
}
