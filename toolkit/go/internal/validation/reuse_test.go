package validation_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func TestValidationHistoryOwnsReuseAndStatistics(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo executed >> runs.out\n\t@echo checked\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}}
	first, err := repokit.Validate(context.Background(), req)
	if err != nil || !first.Passed {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := repokit.Validate(context.Background(), req)
	if err != nil || !second.Passed {
		t.Fatalf("%+v %v", second, err)
	}
	a, b := first.Components[0].Checks[0], second.Components[0].Checks[0]
	if !b.Reused || a.RunID == "" || b.RunID != a.RunID || !b.CheckedAt.Equal(a.CheckedAt) || b.Stdout != a.Stdout || first.InputID == second.InputID {
		t.Fatalf("%+v %+v", a, b)
	}
	if second.History == nil {
		t.Fatal(second.Diagnostics)
	}
	if c := second.History.Checks[". lint"]; c.Executions != 1 || c.Reuses != 1 || c.ExecutionSeconds != a.DurationSeconds {
		t.Fatalf("%+v", c)
	}
	data, _ := json.Marshal(second)
	if strings.Contains(string(data), "fingerprint") {
		t.Fatal("per-check fingerprint survived")
	}
	req.Force = true
	forced, err := repokit.Validate(context.Background(), req)
	if err != nil || !forced.Passed || forced.Components[0].Checks[0].Reused {
		t.Fatalf("%+v %v", forced, err)
	}
	stats, err := repokit.ReadValidationHistory(context.Background(), req.HistoryDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Checks[". lint"].Executions != 2 || stats.Checks[". lint"].Reuses != 1 {
		t.Fatal(stats)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "runs.out"))
	if string(actual) != "executed\nexecuted\n" {
		t.Fatalf("%q", actual)
	}
}

// +case:id=validation_inherited_invocation_identity,desc=`Only the inherited tool call ID changes between checks`,expect=`The child never receives the ID and the second request reuses the first run; build and sandbox settings still invalidate reuse`
func TestValidationInheritedInvocationIdentity(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@printf '%s|%s|%s' \"$${CODEX_TOOL_CALL_ID-unset}\" \"$$VALIDATION_MODE\" \"$$CODEX_SANDBOX\"\n\t@echo executed >> runs.out\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}}
	t.Setenv("CODEX_TOOL_CALL_ID", "call-one")
	t.Setenv("VALIDATION_MODE", "normal")
	t.Setenv("CODEX_SANDBOX", "fixture-a")
	run := func(reused bool, output string) repokit.CheckResult {
		t.Helper()
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || !report.InputsConsistent || len(report.Diagnostics) != 0 {
			t.Fatalf("%+v %v", report, err)
		}
		result := report.Components[0].Checks[0]
		if result.Reused != reused || result.Stdout != output {
			t.Fatalf("reused=%v stdout=%q; want %v %q", result.Reused, result.Stdout, reused, output)
		}
		return result
	}
	first := run(false, "unset|normal|fixture-a")
	t.Setenv("CODEX_TOOL_CALL_ID", "call-two")
	if reused := run(true, "unset|normal|fixture-a"); reused.RunID != first.RunID || reused.Environment != first.Environment {
		t.Fatal("invocation identity changed the run or environment")
	}
	t.Setenv("VALIDATION_MODE", "changed")
	if result := run(false, "unset|changed|fixture-a"); result.ReuseReason != "execution environment changed" {
		t.Fatal(result.ReuseReason)
	}
	t.Setenv("CODEX_SANDBOX", "fixture-b")
	run(false, "unset|changed|fixture-b")
	actual, err := os.ReadFile(filepath.Join(root, "runs.out"))
	if err != nil || string(actual) != "executed\nexecuted\nexecuted\n" {
		t.Fatalf("%q %v", actual, err)
	}
}

// +case:id=validation_explicit_invocation_identity,desc=`A caller explicitly includes a tool call ID in the check environment`,expect=`It reaches the child and changing it requires a new execution`
func TestValidationExplicitInvocationIdentity(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@printf '%s' \"$$CODEX_TOOL_CALL_ID\"\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}, Environment: []string{"PATH=" + os.Getenv("PATH"), "CODEX_TOOL_CALL_ID=explicit-one"}}
	for _, id := range []string{"explicit-one", "explicit-two"} {
		req.Environment[1] = "CODEX_TOOL_CALL_ID=" + id
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || !report.InputsConsistent {
			t.Fatalf("%+v %v", report, err)
		}
		result := report.Components[0].Checks[0]
		if result.Reused || result.Stdout != id {
			t.Fatalf("reused=%v stdout=%q; want execution with %q", result.Reused, result.Stdout, id)
		}
	}
}

func TestValidationHistoryEnvironmentAndScopeInvalidate(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo '$(LINT_FILES)' >> runs.out\n")
	writeValidationFile(t, root, "a.py", "pass")
	writeValidationFile(t, root, "b.py", "pass")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}, Files: []string{"a.py"}, Environment: []string{"PATH=" + os.Getenv("PATH"), "MODE=one"}}
	run := func(reused bool) {
		t.Helper()
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || report.Components[0].Checks[0].Reused != reused {
			t.Fatalf("%+v %v", report, err)
		}
	}
	run(false)
	run(true)
	req.Environment[1] = "MODE=two"
	run(false)
	req.Files = []string{"b.py"}
	run(false)
	run(true)
	writeValidationFile(t, root, "b.py", "# changed")
	run(false)
}

func TestValidationCorruptHistoryRunsChecksAndReportsGap(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo run >> runs.out\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}}
	first, err := repokit.Validate(context.Background(), req)
	if err != nil || !first.Passed {
		t.Fatalf("%+v %v", first, err)
	}
	paths, err := filepath.Glob(filepath.Join(req.HistoryDir, "*", "history", "*"))
	if err != nil || len(paths) == 0 {
		t.Fatal(err)
	}
	file, err := os.OpenFile(paths[len(paths)-1], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("{")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	next, err := repokit.Validate(context.Background(), req)
	if err != nil || !next.Passed || next.Components[0].Checks[0].Reused || len(next.Diagnostics) == 0 || next.History != nil {
		t.Fatalf("%+v %v", next, err)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "runs.out"))
	if string(actual) != "run\nrun\n" {
		t.Fatalf("%q", actual)
	}
}

func TestValidationUnavailableExecutableDoesNotCountAsExecution(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@true\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}, Environment: []string{"PATH=/no-such-path"}}
	report, err := repokit.Validate(context.Background(), req)
	if err != nil || report.Components[0].Checks[0].Status != "unavailable" || report.History == nil || len(report.History.Checks) != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}

// +case:id=repository_history_worktrees,desc=`Two worktrees validate the same repository`,expect=`One history directory aggregates runs while reuse stays within each checkout`
func TestValidationRepositoryHistoryAcrossWorktrees(t *testing.T) {
	for _, origin := range []string{"", "https://github.com/example/shared.git"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			root := validationRepo(t, "lint:\n\t@echo executed >> runs.out\n")
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
			}
			if origin != "" {
				git("remote", "add", "origin", origin)
			}
			git("add", ".")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture")
			linked := filepath.Join(t.TempDir(), "linked")
			git("worktree", "add", "--detach", linked, "HEAD")
			var err error
			linked, err = filepath.EvalSymlinks(linked)
			if err != nil {
				t.Fatal(err)
			}
			history := t.TempDir()
			ids := map[string]string{}
			for i, checkout := range []string{root, linked, root, linked} {
				report, err := repokit.Validate(ctx, repokit.ValidationRequest{Repository: checkout, HistoryDir: history, Checks: []string{"lint"}})
				if err != nil || !report.Passed || !report.InputsConsistent || report.History == nil {
					t.Fatalf("%+v: %v", report, err)
				}
				result := report.Components[0].Checks[0]
				if result.Reused != (i >= 2) {
					t.Fatalf("checkout=%s reused=%v", checkout, result.Reused)
				}
				if i < 2 {
					ids[checkout] = result.RunID
				} else if result.RunID != ids[checkout] {
					t.Fatal("reused another checkout")
				}
			}
			if ids[root] == ids[linked] {
				t.Fatal("cross-checkout run reused")
			}
			for _, checkout := range []string{root, linked} {
				stats, err := repokit.ReadValidationHistory(ctx, history, checkout)
				if err != nil {
					t.Fatal(err)
				}
				counts := stats.Checks[". lint"]
				if counts.Executions != 2 || counts.Reuses != 2 {
					t.Fatalf("%+v", counts)
				}
				actual, err := os.ReadFile(filepath.Join(checkout, "runs.out"))
				if err != nil || string(actual) != "executed\n" {
					t.Fatalf("%s: %v", actual, err)
				}
			}
			entries, err := os.ReadDir(history)
			if err != nil || len(entries) != 1 {
				t.Fatalf("repository directories=%v: %v", entries, err)
			}
			if origin != "" && entries[0].Name() != "github--example--shared" {
				t.Fatal(entries[0].Name())
			}
			stored, err := os.ReadDir(filepath.Join(history, entries[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range stored {
				switch entry.Name() {
				case "history", "inputs", "logs", "lock":
				default:
					t.Fatalf("unexpected per-checkout storage: %s", entry.Name())
				}
			}
		})
	}
}

// +case:id=validation_failure_revokes_success,desc=`A forced run fails after a success with unchanged declared inputs`,expect=`Normal validation executes until a new success is available for reuse`
func TestValidationFailureRevokesSuccess(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo executed >> runs.out\n\t@test ! -e fail.out\ntest:\n\t@true\n")
	req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint", "test"}}
	run := func(passed, reused bool) repokit.CheckResult {
		t.Helper()
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || report.Passed != passed || !report.InputsConsistent || len(report.Diagnostics) != 0 {
			t.Fatalf("%+v %v", report, err)
		}
		for _, result := range report.Components[0].Checks {
			if result.Check == "lint" {
				if result.Reused != reused {
					t.Fatalf("%+v", result)
				}
				return result
			}
		}
		t.Fatal("missing lint result")
		return repokit.CheckResult{}
	}
	first := run(true, false)
	// Ignored state models a transient failure without changing declared inputs.
	writeValidationFile(t, root, "fail.out", "fail")
	req.Force = true
	run(false, false)
	req.Force = false
	failed := run(false, false)
	if failed.Status != "failed" || failed.ReuseReason != "latest matching check did not pass" {
		t.Fatalf("%+v", failed)
	}
	if err := os.Remove(filepath.Join(root, "fail.out")); err != nil {
		t.Fatal(err)
	}
	recovered := run(true, false)
	reused := run(true, true)
	if recovered.RunID == first.RunID || reused.RunID != recovered.RunID {
		t.Fatalf("old=%s recovered=%s reused=%s", first.RunID, recovered.RunID, reused.RunID)
	}
	stats, err := repokit.ReadValidationHistory(context.Background(), req.HistoryDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if c := stats.Checks[". lint"]; c.Executions != 4 || c.Passed != 2 || c.Failed != 2 || c.Reuses != 1 {
		t.Fatalf("lint: %+v", c)
	}
	if c := stats.Checks[". test"]; c.Executions != 2 || c.Reuses != 3 {
		t.Fatalf("unrelated test: %+v", c)
	}
}

// +case:id=validation_latest_matching_run,desc=`A succeeds and a different B fails before checking A again`,expect=`A reuses its own success while B executes after its failure`
func TestValidationUsesLatestMatchingRun(t *testing.T) {
	for _, difference := range []string{"contents", "environment", "scope", "files", "receipt", "declaration"} {
		t.Run(difference, func(t *testing.T) {
			t.Parallel()
			root := validationRepo(t, "lint:\n\t@echo '$(LINT_FILES)' >> runs.out\n\t@test ! -e fail.out\n")
			writeValidationFile(t, root, "a.py", "# A\n")
			writeValidationFile(t, root, "b.py", "# B\n")
			writeValidationFile(t, root, "receipt.out", "A")
			req := repokit.ValidationRequest{Repository: root, HistoryDir: t.TempDir(), Checks: []string{"lint"}, Environment: []string{"PATH=" + os.Getenv("PATH"), "MODE=A"}}
			selectRun := func(b bool) {
				switch difference {
				case "contents":
					contents := "# A\n"
					if b {
						contents = "# changed\n"
					}
					writeValidationFile(t, root, "a.py", contents)
				case "environment":
					req.Environment[1] = "MODE=A"
					if b {
						req.Environment[1] = "MODE=B"
					}
				case "scope":
					req.Files = nil
					if b {
						req.Files = []string{"a.py"}
					}
				case "files":
					req.Files = []string{"a.py"}
					if b {
						req.Files = []string{"b.py"}
					}
				case "receipt":
					req.Inputs = map[string]map[string]repokit.CheckInputDeclaration{".": {"lint": {Witnesses: []string{"receipt.out"}}}}
					contents := "A"
					if b {
						contents = "B"
					}
					writeValidationFile(t, root, "receipt.out", contents)
				case "declaration":
					req.Inputs = nil
					if b {
						req.Inputs = map[string]map[string]repokit.CheckInputDeclaration{".": {"lint": {Components: []string{"."}}}}
					}
				}
			}
			run := func(b, passed, reused bool) repokit.CheckResult {
				t.Helper()
				selectRun(b)
				report, err := repokit.Validate(context.Background(), req)
				if err != nil || report.Passed != passed || !report.InputsConsistent || len(report.Diagnostics) != 0 {
					t.Fatalf("%+v %v", report, err)
				}
				result := report.Components[0].Checks[0]
				wantScope := "full"
				if difference == "files" || difference == "scope" && b {
					wantScope = "focused"
				}
				if result.Scope != wantScope {
					t.Fatalf("scope=%s, want %s: %+v", result.Scope, wantScope, result)
				}
				if difference == "files" && (len(result.Files) != 1 || result.Files[0] != req.Files[0]) {
					t.Fatalf("selected files=%v, want %v", result.Files, req.Files)
				}
				if result.Reused != reused {
					t.Fatalf("%+v", result)
				}
				return result
			}
			a := run(false, true, false)
			writeValidationFile(t, root, "fail.out", "fail")
			wantReason := map[string]string{
				"contents": "check input contents changed", "environment": "execution environment changed",
				"scope": "check command, scope or files changed", "files": "check command, scope or files changed",
				"receipt": "dependency evidence changed", "declaration": "check input declaration changed",
			}[difference]
			if different := run(true, false, false); different.ReuseReason != wantReason {
				t.Fatalf("miss reason=%q, want %q", different.ReuseReason, wantReason)
			}
			if reused := run(false, true, true); reused.RunID != a.RunID {
				t.Fatalf("A reused a different run: %s != %s", reused.RunID, a.RunID)
			}
			if failed := run(true, false, false); failed.ReuseReason != "latest matching check did not pass" {
				t.Fatalf("B ignored its latest failure: %+v", failed)
			}
		})
	}
}
