package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/validation"
)

// ValidationRequest selects Makefile checks and their Component/file scope.
type ValidationRequest = validation.ValidationRequest

// CheckSpec freezes one Component/check invocation, its scope and execution inputs.
type CheckSpec = validation.CheckSpec

// CheckInputDeclaration declares complete check inputs and dependency receipts.
type CheckInputDeclaration = validation.CheckInputDeclaration

// ComponentValidationPlan groups frozen checks by their execution Component.
type ComponentValidationPlan = validation.ComponentValidationPlan

// ValidationPlan is a live, content-bound plan; its JSON is not an executable artifact.
type ValidationPlan = validation.ValidationPlan

// CheckResult records command outcome independently of round-level consistency.
type CheckResult = validation.CheckResult

// ComponentValidationResult groups check outcomes by execution Component.
type ComponentValidationResult = validation.ComponentValidationResult

// ValidationReport records execution and input consistency, not merge permission.
type ValidationReport = validation.ValidationReport

// PlanValidation reads the checkout and freezes Makefile checks without executing them.
func PlanValidation(ctx context.Context, req ValidationRequest) (ValidationPlan, error) {
	result, err := validation.PlanValidation(ctx, req)
	return result, errs.Wrap(err, "validation.plan_failed", "validation.plan", req.Repository)
}

// ExecuteValidation runs a live frozen plan and verifies its input consistency.
func ExecuteValidation(ctx context.Context, plan ValidationPlan) (ValidationReport, error) {
	result, err := validation.ExecuteValidation(ctx, plan)
	return result, errs.Wrap(err, "validation.execution_failed", "validation.execute", plan.Checkout)
}

// Validate completes optional formatting before planning and executing lint/test.
func Validate(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	result, err := validation.Validate(ctx, req)
	return result, errs.Wrap(err, "validation.failed", "validation", req.Repository)
}

// Format runs formatting without granting lint/test evidence.
func Format(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	result, err := validation.Format(ctx, req)
	return result, errs.Wrap(err, "validation.format_failed", "validation.format", req.Repository)
}

// Lint plans and executes only lint checks.
func Lint(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	result, err := validation.Lint(ctx, req)
	return result, errs.Wrap(err, "validation.lint_failed", "validation.lint", req.Repository)
}

// Test plans and executes only test checks.
func Test(ctx context.Context, req ValidationRequest) (ValidationReport, error) {
	result, err := validation.Test(ctx, req)
	return result, errs.Wrap(err, "validation.test_failed", "validation.test", req.Repository)
}

// ValidationHistory summarizes retained actual executions and reuse requests.
type ValidationHistory = validation.ValidationHistory
type HistoryCounts = validation.HistoryCounts

// ValidationHistories contains retained per-repository statistics and read diagnostics.
type ValidationHistories = validation.ValidationHistories

// ReadValidationHistories reads existing stores without requiring their checkouts.
// Missing directories are empty; unreadable stores are excluded with diagnostics.
func ReadValidationHistories(ctx context.Context, directory string) (ValidationHistories, error) {
	result, err := validation.ReadValidationHistories(ctx, directory)
	return result, errs.Wrap(err, "validation.history_failed", "validation.history", directory)
}

// ReadValidationHistory reads statistics without running checks.
func ReadValidationHistory(ctx context.Context, directory, checkout string) (ValidationHistory, error) {
	result, err := validation.ReadValidationHistory(ctx, directory, checkout)
	return result, errs.Wrap(err, "validation.history_failed", "validation.history", checkout)
}
