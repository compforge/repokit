package validation_test

import (
	"context"
	"os"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=Worker overrides beat ordinary Makefile assignments, preserve focused scope, and leave format and omitted budgets unchanged.
// +link=component://docs/validation.md
func TestValidationWorkerBudgets(t *testing.T) {
	root := validationRepo(t, "LINT_WORKERS := 1\nTEST_WORKERS := 1\nformat lint test:\n\t@printf '%s|%s' '$(LINT_WORKERS)' '$(TEST_WORKERS)'\n\t@echo '$(LINT_FILES)$(TEST_FILES)' > /dev/null\n")
	writeValidationFile(t, root, "a.py", "pass\n")
	req := repokit.ValidationRequest{Repository: root, Checks: []string{"format", "lint", "test"}, Files: []string{"a.py"}, LintWorkers: 3, TestWorkers: 4, Environment: []string{"PATH=" + os.Getenv("PATH")}}
	report, err := repokit.Validate(context.Background(), req)
	if err != nil || !report.Passed || !report.Complete || !report.InputsConsistent {
		t.Fatalf("%+v %v", report, err)
	}
	if got := report.Preparation[0].Checks[0].Stdout; got != "1|1" {
		t.Fatalf("format received worker overrides: %q", got)
	}
	for _, check := range report.Components[0].Checks {
		want := "3|1"
		if check.Check == "test" {
			want = "1|4"
		}
		if check.Stdout != want || check.Scope != "focused" || len(check.Files) != 1 || check.Files[0] != "a.py" {
			t.Fatalf("unexpected worker execution: %+v", check)
		}
	}
	req.LintWorkers, req.TestWorkers = 0, 0
	report, err = repokit.Validate(context.Background(), req)
	if err != nil || !report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	for _, check := range report.Components[0].Checks {
		if check.Stdout != "1|1" {
			t.Fatalf("omitted budget replaced project default: %+v", check)
		}
	}
}

// +case=Changing one worker budget reruns only that check; unchanged budgets and returning to an earlier budget reuse matching successful runs.
// +link=component://docs/validation.md
func TestValidationWorkerReuse(t *testing.T) {
	root := validationRepo(t, "lint test:\n\t@printf '%s|%s' '$(LINT_WORKERS)' '$(TEST_WORKERS)'\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Environment: []string{"PATH=" + os.Getenv("PATH")}}
	rounds := []struct {
		lint, test           int
		reuseLint, reuseTest bool
	}{
		{2, 2, false, false}, {2, 2, true, true}, {2, 4, true, false}, {4, 4, false, true}, {2, 4, true, true},
	}
	for _, round := range rounds {
		req.LintWorkers, req.TestWorkers = round.lint, round.test
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || !report.InputsConsistent {
			t.Fatalf("%+v %v", report, err)
		}
		for _, check := range report.Components[0].Checks {
			want := round.reuseLint
			if check.Check == "test" {
				want = round.reuseTest
			}
			if check.Reused != want {
				t.Fatalf("round %+v: %s reused=%v want %v (%s)", round, check.Check, check.Reused, want, check.ReuseReason)
			}
		}
	}
}

func TestValidationRejectsNegativeWorkerBudgets(t *testing.T) {
	for _, req := range []repokit.ValidationRequest{{LintWorkers: -1}, {TestWorkers: -1}} {
		if _, err := repokit.PlanValidation(context.Background(), req); err == nil || !strings.Contains(err.Error(), "worker budgets") {
			t.Fatalf("invalid workers reached repository inspection: %v", err)
		}
	}
}
