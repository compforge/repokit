package validation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=Repeated builds recreate a missing artifact and incorporate VERSION even without code changes.
func TestValidationBuildCurrentArtifact(t *testing.T) {
	ctx := context.Background()
	root := validationRepo(t, ".PHONY: build\nbuild:\n\t@cat VERSION > artifact.out\n")
	writeValidationFile(t, root, "VERSION", "1\n")
	validationChild(t, root, "child", "build-all:\n\t@false\n")
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Components: []string{"."}, Checks: []string{"build"}, Base: "HEAD"}
	for _, version := range []string{"1\n", "1\n", "2\n"} {
		writeValidationFile(t, root, "VERSION", version)
		report, err := repokit.Validate(ctx, req)
		if err != nil || !report.Passed || !report.InputsConsistent {
			t.Fatal(report, err)
		}
		check := report.Components[0].Checks[0]
		if check.Status != "passed" || check.Reused || check.Scope != "full" || check.RunID == "" || len(check.Files) != 0 {
			t.Fatal(check)
		}
		artifact := filepath.Join(root, "artifact.out")
		if data, err := os.ReadFile(artifact); err != nil || string(data) != version {
			t.Fatalf("artifact %q: %v", data, err)
		}
		if err := os.Remove(artifact); err != nil {
			t.Fatal(err)
		}
	}
	history, err := repokit.ListValidationRuns(ctx, repokit.ValidationRunQuery{Repository: root, HistoryDir: req.HistoryDir, Check: "build"})
	if err != nil || history.Total != 3 {
		t.Fatal(history, err)
	}
	req.Components = nil
	plan, err := repokit.PlanValidation(ctx, req)
	if err != nil || len(plan.Components) != 2 || plan.Components[0].Checks[0].Scope != "full" || plan.Components[1].Checks[0].Scope != "skipped" {
		t.Fatal(plan, err)
	}
	req.Components = []string{"child"}
	report, err := repokit.Validate(ctx, req)
	if err != nil || report.Components[0].Checks[0].Status != "skipped" || report.Components[0].Checks[0].RunID != "" {
		t.Fatal(report, err)
	}
}

// +case=By default lint, test and build can all start after format before any of them finishes.
func TestValidationConcurrentBuild(t *testing.T) {
	root := validationRepo(t, `format:
	@echo formatted > VERSION
lint test build:
	@grep -qx formatted VERSION
	@touch $@.out
	@while [ ! -f lint.out ] || [ ! -f test.out ] || [ ! -f build.out ]; do sleep 0.05; done
`)
	writeValidationFile(t, root, "VERSION", "initial\n")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	report, err := repokit.Validate(ctx, repokit.ValidationRequest{
		Repository: root, Checks: []string{"format", "lint", "test", "build"},
	})
	if err != nil || !report.Passed || !report.Complete || !report.InputsConsistent {
		t.Fatal(report, err)
	}
	for i, name := range []string{"lint", "test", "build"} {
		check := report.Components[0].Checks[i]
		if check.Check != name || check.Status != "passed" {
			t.Fatal(check)
		}
	}
}

// +case=Queued builds still execute when a sibling fails; changed inputs invalidate the round.
func TestValidationBuildIndependentResult(t *testing.T) {
	for _, tc := range []struct {
		name, recipe, status string
		consistent           bool
	}{
		{"failed test", "false", "failed", true},
		{"changed inputs", "echo changed > VERSION", "passed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := validationRepo(t, "test:\n\t@"+tc.recipe+"\nbuild:\n\t@touch build.out\n")
			writeValidationFile(t, root, "VERSION", "initial\n")
			report, err := repokit.Validate(t.Context(), repokit.ValidationRequest{
				Repository: root, Checks: []string{"test", "build"}, Concurrency: 1,
			})
			if err != nil || report.Passed || !report.Complete || report.InputsConsistent != tc.consistent {
				t.Fatal(report, err)
			}
			checks := report.Components[0].Checks
			if checks[0].Status != tc.status || checks[1].Status != "passed" || checks[1].RunID == "" {
				t.Fatal(checks)
			}
		})
	}
}

// +case=Optional file and worker parameters reach only their Component's declared recipe.
func TestArtifactMakeParameters(t *testing.T) {
	root := validationRepo(t, "format:\n\t@echo '$(FORMAT_FILES)|$(FORMAT_WORKERS)'\nbuild:\n\t@echo '$(BUILD_FILES)|$(BUILD_WORKERS)'\n")
	writeValidationFile(t, root, "file.py", "pass\n")
	request := repokit.ValidationRequest{Repository: root, Checks: []string{"build", "format"}, Files: []string{"file.py"}, FormatWorkers: 2, BuildWorkers: 3}
	report, err := repokit.Validate(context.Background(), request)
	if err != nil || !report.Passed {
		t.Fatal(report, err)
	}
	format, build := report.Preparation[0].Checks[0], report.Components[0].Checks[0]
	if format.Scope != "focused" || format.Stdout != "file.py|2\n" || build.Scope != "focused" || build.Stdout != "file.py|3\n" {
		t.Fatal(format, build)
	}
}
