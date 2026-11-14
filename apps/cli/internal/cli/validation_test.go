package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

func TestValidationCLIPlansAndRunsMake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, ".gitignore", "*.out\n")
	put(t, root, "Makefile", "lint:\n\t@echo lint > lint.out\ntest:\n\t@echo test > test.out\n")
	for _, name := range []string{"lint", "test", "validate"} {
		t.Run(name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), []string{name, "--repo", root, "--plan"}, nil, &out, &stderr); code != 0 {
				t.Fatalf("%d: %s", code, &stderr)
			}
			var plan repokit.ValidationPlan
			if err := json.Unmarshal(resultData(t, out.Bytes()), &plan); err != nil {
				t.Fatal(err)
			}
			want := 1
			if name == "validate" {
				want = 2
			}
			if len(plan.Components) != 1 || len(plan.Components[0].Checks) != want {
				t.Fatalf("%s", &out)
			}
			out.Reset()
			stderr.Reset()
			if code := Execute(context.Background(), []string{name, "--repo", root, "--json"}, nil, &out, &stderr); code != 0 {
				t.Fatalf("%d: %s", code, &stderr)
			}
			var report repokit.ValidationReport
			if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
				t.Fatal(err)
			}
			if !report.Complete || !report.Passed || len(report.Components[0].Checks) != want {
				t.Fatalf("%s", &out)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "test.out")); err != nil {
		t.Fatal(err)
	}
	put(t, root, "Makefile", "test:\n\t@false\n")
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"test", "--repo", root, "--json"}, nil, &out, &stderr); code != 1 {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"test", "--repo", root, "--timeout=0"}, nil, &out, &stderr); code != 2 {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
}

func TestValidationCLIPlanDoesNotRunProjectCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, "Makefile", "EXPAND := $(shell touch expanded.out)\ntest:\n\t@touch ran.out\n")
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"test", "--repo", root, "--plan"}, nil, &out, &stderr); code != 0 {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
	for _, name := range []string{"expanded.out", "ran.out"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("planning executed %s", name)
		}
	}
}

func TestValidationCLIFormatCompositionAndReuseReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, ".gitignore", "*.out\n")
	put(t, root, "source.py", "not formatted")
	put(t, root, "Makefile", "format:\n\t@echo formatted > source.py\nlint test:\n\t@grep -q formatted source.py\n")
	run := func(args ...string) (int, repokit.ValidationReport) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), append(args, "--repo", root, "--json"), nil, &out, &stderr)
		var report repokit.ValidationReport
		if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
			t.Fatalf("%d %s %s %v", code, &out, &stderr, err)
		}
		var result commandResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Cost.WallSeconds <= 0 || result.Cost.Validation == nil || len(result.Cost.Phases) == 0 || result.Cost.Phases[0].Name != "validation" {
			t.Fatalf("missing measured cost: %s", &out)
		}
		for _, step := range result.Cost.Validation.Steps {
			if step.Reused && step.ExecutionSeconds != 0 {
				t.Fatalf("charged historical execution: %+v", step)
			}
		}
		return code, report
	}
	if code, report := run("format"); code != 0 || len(report.Components) != 0 || len(report.Preparation) != 1 {
		t.Fatalf("%d %+v", code, report)
	}
	previous := filepath.Join(root, "previous.out")
	code, report := run("validate", "--check", "test,format,lint", "--output", previous)
	if code != 0 || !report.InputsConsistent || len(report.Preparation) != 1 {
		t.Fatalf("%d %+v", code, report)
	}
	code, report = run("validate")
	if code != 0 || !report.Components[0].Checks[0].Reused || !report.Components[0].Checks[1].Reused {
		t.Fatalf("%d %+v", code, report)
	}
	put(t, root, "Makefile", "format:\n\t@false\nlint:\n\t@touch unexpected.out\n")
	code, report = run("validate", "--check", "format,lint")
	if code != 1 || len(report.Components) != 0 {
		t.Fatalf("%d %+v", code, report)
	}
	if _, err := os.Stat(filepath.Join(root, "unexpected.out")); !os.IsNotExist(err) {
		t.Fatal("lint ran after failed format")
	}
}

func TestValidationCLIMissingTargetsSkipExplicitComponents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, ".gitignore", "*.out\n")
	put(t, root, "Makefile", "test:\n\t@echo run >> runs.out\n")
	for _, child := range []string{"a", "b"} {
		put(t, root, child+"/pyproject.toml", "[project]\nname='"+child+"'\nversion='0.1.0'\n")
	}
	var out, stderr bytes.Buffer
	args := []string{"test", "--repo", root, "--component", "a", "--component", "b", "--json"}
	if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
	var report repokit.ValidationReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Passed || len(report.Components) != 2 || !strings.Contains(out.String(), "No checks executed; 2 skipped") {
		t.Fatalf("%s", &out)
	}
	for _, group := range report.Components {
		if group.Component.Root == "." || len(group.Checks) != 1 || group.Checks[0].Status != "skipped" || group.Checks[0].Witnesses == nil {
			t.Fatal(group)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "runs.out")); !os.IsNotExist(err) {
		t.Fatal("root executed", err)
	}
}

func TestValidationCLIHistoryQueryAndPlanAreNotExecutions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, history := t.TempDir(), filepath.Join(t.TempDir(), "history")
	if data, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", data, err)
	}
	put(t, root, "Makefile", "lint:\n\t@echo run >> runs.out\n")
	put(t, root, ".gitignore", "*.out\n")
	var out, stderr bytes.Buffer
	run := func(args ...string) {
		t.Helper()
		out.Reset()
		stderr.Reset()
		args = append(args, "--repo", root, "--history-dir", history, "--json")
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 {
			t.Fatalf("%d %s %s", code, &out, &stderr)
		}
	}
	run("validate", "--check", "lint", "--plan")
	if _, err := os.Stat(history); !os.IsNotExist(err) {
		t.Fatalf("plan created history: %v", err)
	}
	run("validate", "--check", "lint")
	run("validate", "--check", "lint")
	run("validate", "--history")
	var summary repokit.ValidationHistory
	if err := json.Unmarshal(resultData(t, out.Bytes()), &summary); err != nil {
		t.Fatal(err)
	}
	if c := summary.Checks[". lint"]; c.Executions != 1 || c.Reuses != 1 {
		t.Fatalf("%+v", c)
	}
	data, _ := os.ReadFile(filepath.Join(root, "runs.out"))
	if string(data) != "run\n" {
		t.Fatalf("%q", data)
	}
}

// +case:id=validation_without_home,desc=`HOME is unavailable`,expect=`Plans and checks succeed without history; explicit history storage still works`
func TestValidationCLIWithoutHome(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, ".gitignore", "*.out\n")
	put(t, root, "Makefile", "format lint test:\n\t@echo ran >> runs.out\n")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	for _, name := range []string{"format", "lint", "test", "validate"} {
		t.Run(name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), []string{name, "--repo", root, "--plan"}, nil, &out, &stderr); code != 0 {
				t.Fatalf("plan exit=%d: %s", code, &stderr)
			}
			out.Reset()
			stderr.Reset()
			if code := Execute(context.Background(), []string{name, "--repo", root, "--json", "--force"}, nil, &out, &stderr); code != 0 {
				t.Fatalf("run exit=%d: %s", code, &stderr)
			}
			var report repokit.ValidationReport
			if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
				t.Fatal(err)
			}
			if !report.Passed || (name != "format" && !report.InputsConsistent) || report.History != nil {
				t.Fatalf("%s", &out)
			}
			if !strings.Contains(out.String(), "validation.history_unavailable") {
				t.Fatalf("missing warning: %s", &stderr)
			}
		})
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"validate", "--repo", root, "--history"}, nil, &out, &stderr); code != 1 || !strings.Contains(out.String(), "--history-dir") || string(resultData(t, out.Bytes())) != "null" {
		t.Fatalf("query exit=%d: stdout=%s stderr=%s", code, &out, &stderr)
	}
	history := t.TempDir()
	for i := 0; i < 2; i++ {
		out.Reset()
		stderr.Reset()
		if code := Execute(context.Background(), []string{"lint", "--repo", root, "--json", "--history-dir", history}, nil, &out, &stderr); code != 0 {
			t.Fatalf("explicit exit=%d: %s", code, &stderr)
		}
		var report repokit.ValidationReport
		if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
			t.Fatal(err)
		}
		if report.Components[0].Checks[0].Reused != (i == 1) {
			t.Fatalf("reuse: %s", &out)
		}
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"validate", "--repo", root, "--history", "--history-dir", history}, nil, &out, &stderr); code != 0 {
		t.Fatalf("explicit query exit=%d: %s", code, &stderr)
	}
}

// +case=An export failure preserves completed validation results and measured phases
func TestValidationExportFailurePreservesReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, "Makefile", "test:\n\t@echo check-completed\n")
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", "--repo", root, "--json", "--output", filepath.Join(t.TempDir(), "missing", "report.json")}, nil, &out, &stderr)
	var result commandResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var report repokit.ValidationReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if code != 1 || stderr.Len() != 0 || !report.Passed || result.Error == nil || result.Error.Op != "export" || len(result.Artifacts) != 0 {
		t.Fatalf("code=%d result=%s stderr=%s", code, &out, &stderr)
	}
	if result.Cost.Validation == nil || result.Cost.Validation.ExecutionSeconds <= 0 || len(result.Cost.Phases) != 1 || result.Cost.Phases[0].Name != "validation" {
		t.Fatalf("lost cost: %s", &out)
	}
}

// +case=Execution, planning, and failure exports share the complete stdout JSON contract
func TestValidationExportsFinalResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	put(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='0.1.0'\n")
	put(t, root, "Makefile", "test:\n\t@echo check-completed\n")
	for _, tc := range []struct {
		name  string
		flags []string
		code  int
	}{
		{"execution", nil, 0},
		{"plan", []string{"--plan"}, 0},
		{"history", []string{"--history"}, 0},
		{"argument-failure", []string{"--timeout", "0s"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			args := append([]string{"test", "--repo", root, "--json", "--output", path}, tc.flags...)
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), args, nil, &out, &stderr); code != tc.code || stderr.Len() != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, out.Bytes()) {
				t.Fatalf("different export: %v file=%s stdout=%s", err, data, &out)
			}
			resultData(t, data)
			var result commandResult
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Cost.WallSeconds <= 0 || len(result.Artifacts) != 1 || result.Artifacts[0].Path != path || result.Artifacts[0].Kind != "command_result" {
				t.Fatalf("missing final metadata: %s", data)
			}
		})
	}
}
