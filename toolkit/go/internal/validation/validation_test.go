package validation_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func validationRepo(t *testing.T, makefile string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	for path, text := range map[string]string{"pyproject.toml": "[project]\nname = 'fixture'\nversion = '0.1.0'\n", "Makefile": makefile, ".gitignore": "*.out\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func validationPlan(t *testing.T, root string, checks ...string) repokit.ValidationPlan {
	t.Helper()
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestMakeValidationConformance(t *testing.T) {
	data, err := os.ReadFile("../../../../conformance/validation/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Makefile, Check, Scope string
		Files, Selected, Command     []string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			root := validationRepo(t, tc.Makefile)
			for _, file := range tc.Files {
				if err := os.WriteFile(filepath.Join(root, file), []byte("# fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{tc.Check}, Files: tc.Selected})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Components) != 1 || len(plan.Components[0].Checks) != 1 {
				t.Fatalf("unexpected plan: %+v", plan)
			}
			check := plan.Components[0].Checks[0]
			if check.Scope != tc.Scope || !reflect.DeepEqual(check.Command, tc.Command) {
				t.Fatalf("check=%+v", check)
			}
		})
	}
}

func TestValidationFrozenEnvironmentAndScope(t *testing.T) {
	root := validationRepo(t, "test:\n\t@printf '%s|%s|%s' '$(TEST_FILES)' '$(LINT_FILES)' \"$$VALIDATION_TOKEN\" > result.out\n")
	if err := os.WriteFile(filepath.Join(root, "GNUmakefile"), []byte("test:\n\t@echo wrong > result.out\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAKEFLAGS", "-n")
	t.Setenv("TEST_FILES", "unexpected.py")
	t.Setenv("VALIDATION_TOKEN", "original")
	plan := validationPlan(t, root, "test")
	t.Setenv("VALIDATION_TOKEN", "changed")
	report, err := repokit.ExecuteValidation(context.Background(), plan)
	if err != nil || !report.Complete || !report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	output, err := os.ReadFile(filepath.Join(root, "result.out"))
	if err != nil || string(output) != "||original" {
		t.Fatalf("%s %v", output, err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "original") {
		t.Fatal("environment secret in serialized plan")
	}
	plan.Components[0].Checks[0].Scope = "focused"
	if _, err := repokit.ExecuteValidation(context.Background(), plan); err == nil {
		t.Fatal("mutated plan executed")
	}
}

func TestValidationRoundConsistency(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		t.Run(phase, func(t *testing.T) {
			target := "test:\n\t@echo ran > result.out\n"
			if phase == "during" {
				target += "\t@echo changed >> source.py\n"
			}
			root := validationRepo(t, target)
			if err := os.WriteFile(filepath.Join(root, "source.py"), []byte("# source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			plan := validationPlan(t, root, "test")
			if phase == "before" {
				if err := os.WriteFile(filepath.Join(root, "source.py"), []byte("# changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := repokit.ExecuteValidation(context.Background(), plan)
			if err != nil || report.InputsConsistent || report.Passed || len(report.Diagnostics) == 0 {
				t.Fatalf("%+v %v", report, err)
			}
			if phase == "before" {
				if _, err := os.Stat(filepath.Join(root, "result.out")); !os.IsNotExist(err) {
					t.Fatal("stale plan ran")
				}
			}
		})
	}
}

func TestValidationFailureAndMissingTarget(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo failure >&2\n\t@exit 7\n")
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root})
	if err != nil || report.Passed || !report.Complete || !report.InputsConsistent {
		t.Fatalf("%+v %v", report, err)
	}
	checks := report.Components[0].Checks
	if checks[0].Status != "failed" || !strings.Contains(checks[0].Stderr, "failure") || checks[1].Status != "skipped" {
		t.Fatalf("%+v", checks)
	}
}

func TestValidationTimeoutTerminatesDescendants(t *testing.T) {
	root := validationRepo(t, "test:\n\t@(while [ ! -e release.out ]; do sleep 0.05; done; echo escaped > escaped.out) & wait\n")
	plan := validationPlan(t, root, "test")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	report, err := repokit.ExecuteValidation(ctx, plan)
	if err != nil || report.Complete || report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	if report.Components[0].Checks[0].Status != "uncertain" {
		t.Fatalf("%+v", report)
	}
	if err := os.WriteFile(filepath.Join(root, "release.out"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "escaped.out")); !os.IsNotExist(err) {
		t.Fatal("Make descendant escaped cancellation")
	}
}

func TestValidationNestedOwnershipAndExplicitBoundaries(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo $(TEST_FILES)\n")
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{"child/pyproject.toml": "[project]\nname='child'\nversion='0.1.0'\n", "child/Makefile": "test:\n\t@echo $(TEST_FILES)\n", "child/a.py": "# child\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"."}, Checks: []string{"test"}, Files: []string{"child/a.py"}})
	if err == nil || !strings.Contains(err.Error(), "outside selected Components") {
		t.Fatalf("%+v %v", plan, err)
	}
	if _, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"missing"}}); err == nil {
		t.Fatal("unknown component accepted")
	}
	empty, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{}})
	if err != nil || len(empty.Components) != 0 {
		t.Fatalf("empty selection: %+v %v", empty, err)
	}
}

func TestValidationImpactAndStaleReport(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo $(LINT_FILES)\ntest:\n\t@echo $(TEST_FILES)\n")
	if err := os.WriteFile(filepath.Join(root, "source.py"), []byte("value = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "source.py"), []byte("value = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	impact, err := repokit.AnalyzeImpact(context.Background(), repokit.DiffRequest{Repository: root, Base: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Impact: &impact})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Components[0].Checks[0].Scope != "focused" {
		t.Fatalf("%+v", plan)
	}
	skipped, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Impact: &impact, Tags: []string{}})
	if err != nil || skipped.Components[0].Checks[0].Scope != "skipped" {
		t.Fatalf("%+v %v", skipped, err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.py"), []byte("value = 3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Impact: &impact}); err == nil {
		t.Fatal("stale report accepted")
	}
}

func TestInheritedGitContextDoesNotRedirectMake(t *testing.T) {
	other := validationRepo(t, "test:\n\t@true\n")
	root := validationRepo(t, "test:\n\t@git rev-parse --show-toplevel > actual.out\n")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"test"}})
	if err != nil || !report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	output, err := os.ReadFile(filepath.Join(root, "actual.out"))
	if err != nil || strings.TrimSpace(string(output)) != root {
		t.Fatalf("%s %v", output, err)
	}
}
