package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func phaseNames(t *testing.T, phases []repokit.ValidationPhase, wall time.Duration) string {
	t.Helper()
	var names []string
	total := 0.0
	for _, phase := range phases {
		if phase.DurationSeconds < 0 {
			t.Fatal(phase)
		}
		names = append(names, phase.Name)
		total += phase.DurationSeconds
	}
	if total > wall.Seconds() {
		t.Fatalf("overlapping phases: %fs > %s", total, wall)
	}
	return strings.Join(names, ",")
}

// +case=Combined format and validation retain separate measured stages; reuse measures this invocation and timing observations do not alter the frozen plan.
func TestValidationPhasesAndReuse(t *testing.T) {
	root := validationRepo(t, "format:\n\t@true\ntest:\n\t@true\n")
	req := repokit.ValidationRequest{Repository: root, Checks: []string{"format", "test"}, HistoryDir: t.TempDir()}
	const planning = "planning.snapshot,planning.layout,planning.checks,planning.inputs"
	const execution = "inputs.before,history.open,checks,inputs.after,history.finish"
	const format = "format.planning.snapshot,format.planning.layout,format.planning.checks,format.planning.inputs,format.inputs.before,format.checks,format.inputs.after"
	for i := range 2 {
		started := time.Now()
		report, err := repokit.Validate(context.Background(), req)
		wall := time.Since(started)
		if err != nil || !report.Passed || !report.InputsConsistent {
			t.Fatal(report, err)
		}
		if got := phaseNames(t, report.Phases, wall); got != format+","+planning+","+execution {
			t.Fatal(got)
		}
		if report.Components[0].Checks[0].Reused != (i == 1) {
			t.Fatal(report.Components)
		}
	}
	req.Checks = []string{"test"}
	started := time.Now()
	plan, err := repokit.PlanValidation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := phaseNames(t, plan.Phases, time.Since(started)); got != planning {
		t.Fatal(got)
	}
	plan.Phases = []repokit.ValidationPhase{{Name: "caller observation", DurationSeconds: 99999}}
	started = time.Now()
	report, err := repokit.ExecuteValidation(context.Background(), plan)
	if err != nil || !report.Passed {
		t.Fatal(report, err)
	}
	if got := phaseNames(t, report.Phases, time.Since(started)); got != execution {
		t.Fatal(got)
	}
	// A stale plan stops before checks but still retains measured input verification.
	if err := os.WriteFile(filepath.Join(root, "source.py"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = repokit.ExecuteValidation(context.Background(), plan)
	if err != nil || report.Passed || len(report.Phases) != 1 || report.Phases[0].Name != "inputs.before" {
		t.Fatal(report, err)
	}
}

func TestValidationPhaseFailures(t *testing.T) {
	root := validationRepo(t, "format:\n\t@false\ntest:\n\t@true\n")
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"format", "test"}})
	if err != nil || report.Passed || len(report.Phases) == 0 {
		t.Fatal(report, err)
	}
	for _, phase := range report.Phases {
		if !strings.HasPrefix(phase.Name, "format.") {
			t.Fatal(phase)
		}
	}
	// Planning failure still reports the attempted phase to API and CLI callers.
	report, err = repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: filepath.Join(root, "missing")})
	if err == nil || len(report.Phases) != 1 || report.Phases[0].Name != "planning.snapshot" {
		t.Fatal(report, err)
	}
}
