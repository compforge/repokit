package validation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func fallbackChild(t *testing.T, root, child, makefile string) {
	t.Helper()
	writeValidationFile(t, root, child+"/pyproject.toml", "[project]\nname='"+strings.ReplaceAll(child, "/", "-")+"'\nversion='0.1.0'\n")
	writeValidationFile(t, root, child+"/code.py", "pass\n")
	if makefile != "" {
		writeValidationFile(t, root, child+"/Makefile", makefile)
	}
}

// +case:id=root_fallback_dedup,desc=`Two leaf Components fall back to one root check`,expect=`Root executes once and records both requested Components`
func TestValidationRootFallbackDeduplicatesAndReuses(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo run >> runs.out\n")
	fallbackChild(t, root, "a", "")
	fallbackChild(t, root, "b", "")
	req := repokit.ValidationRequest{HistoryDir: t.TempDir(), Repository: root, Components: []string{"b", "a"}, Checks: []string{"test"}, Inputs: map[string]map[string]repokit.CheckInputDeclaration{".": {"test": {Components: []string{"."}}}}}
	first, err := repokit.Validate(context.Background(), req)
	if err != nil || !first.Passed || len(first.Components) != 1 || first.Components[0].Component.Root != "." {
		t.Fatalf("%+v %v", first, err)
	}
	check := first.Components[0].Checks[0]
	if !reflect.DeepEqual(check.RequestedComponents, []string{"a", "b"}) || check.Scope != "full" {
		t.Fatalf("%+v", check)
	}
	runs, _ := os.ReadFile(filepath.Join(root, "runs.out"))
	if string(runs) != "run\n" {
		t.Fatalf("duplicate root execution: %q", runs)
	}
	second, err := repokit.Validate(context.Background(), req)
	if err != nil || !second.Passed || !second.Components[0].Checks[0].Reused {
		t.Fatalf("%+v %v", second, err)
	}
	writeValidationFile(t, root, "a/code.py", "# changed\n")
	third, err := repokit.Validate(context.Background(), req)
	if err != nil || !third.Passed || third.Components[0].Checks[0].Reused {
		t.Fatalf("nested input escaped root evidence: %+v %v", third, err)
	}
}

func TestValidationFallbackResolvesChecksIndependently(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@echo root-lint > root.out\ntest:\n\t@echo root-test > test.out\nformat:\n\t@echo root-format > format.out\n")
	fallbackChild(t, root, "child", "test:\n\t@echo child-test > child.out\n")
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"lint", "test", "format"}})
	if err != nil || len(plan.Components) != 2 {
		t.Fatalf("%+v %v", plan.Components, err)
	}
	if plan.Components[0].Component.Root != "." || len(plan.Components[0].Checks) != 1 || plan.Components[0].Checks[0].Check != "lint" {
		t.Fatalf("%+v", plan.Components)
	}
	child := plan.Components[1]
	if child.Checks[0].Check != "format" || child.Checks[0].Scope != "unavailable" || child.Checks[1].Check != "test" || child.Checks[1].Scope != "full" {
		t.Fatalf("%+v", child)
	}
	// A declared child failure must not be replaced by the passing root target.
	writeValidationFile(t, root, "child/Makefile", "test:\n\t@false\n")
	report, err := repokit.Test(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}})
	if err != nil || report.Passed || report.Components[0].Component.Root != "child" {
		t.Fatalf("%+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(root, "test.out")); !os.IsNotExist(err) {
		t.Fatal("failed child target fell back to root")
	}
}

func TestValidationFallbackImpactMergesFilesAndFullDominates(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo $(TEST_FILES) >> runs.out\n")
	for _, child := range []string{"a", "b"} {
		fallbackChild(t, root, child, "")
		writeValidationFile(t, root, child+"/code_test.py", "pass\n")
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	for _, child := range []string{"a", "b"} {
		writeValidationFile(t, root, child+"/code_test.py", "# changed\n")
	}
	impact, err := repokit.AnalyzeImpact(context.Background(), repokit.DiffRequest{Repository: root, Base: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	// Keep analysis identity/ownership, and explicitly select the two test paths.
	for i := range impact.Components {
		c := &impact.Components[i]
		if c.Snapshot == "after" && c.Root != "." {
			c.TestFiles = []string{c.Root + "/code_test.py"}
		}
	}
	req := repokit.ValidationRequest{HistoryDir: t.TempDir(), Repository: root, Components: []string{"a", "b"}, Checks: []string{"test"}, Impact: &impact}
	plan, err := repokit.PlanValidation(context.Background(), req)
	if err != nil || len(plan.Components) != 1 {
		t.Fatalf("%+v %v", plan.Components, err)
	}
	check := plan.Components[0].Checks[0]
	if check.Scope != "focused" || !reflect.DeepEqual(check.Files, []string{"a/code_test.py", "b/code_test.py"}) {
		t.Fatalf("%+v", check)
	}
	if check.Command[5] != "TEST_FILES=a/code_test.py b/code_test.py" {
		t.Fatalf("%+v", check.Command)
	}
	report, err := repokit.ExecuteValidation(context.Background(), plan)
	if err != nil || !report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	runs, _ := os.ReadFile(filepath.Join(root, "runs.out"))
	if string(runs) != "a/code_test.py b/code_test.py\n" {
		t.Fatalf("%q", runs)
	}
	// A skipped root request must not widen the focused fallback requests.
	req.Components = nil
	plan, err = repokit.PlanValidation(context.Background(), req)
	if err != nil || plan.Components[0].Checks[0].Scope != "focused" {
		t.Fatalf("%+v %v", plan.Components, err)
	}
	// One source without usable test candidates requires the merged root check in full.
	for i := range impact.Components {
		if impact.Components[i].Root == "b" {
			impact.Components[i].TestFiles = nil
		}
	}
	req.Components = []string{"a", "b"}
	plan, err = repokit.PlanValidation(context.Background(), req)
	if err != nil || plan.Components[0].Checks[0].Scope != "full" || len(plan.Components[0].Checks[0].Files) != 0 {
		t.Fatalf("%+v %v", plan.Components, err)
	}
	// No filtering contract on root means the merged request must remain full.
	writeValidationFile(t, root, "Makefile", "test:\n\t@true\n")
	req.Impact = nil
	req.Files = []string{"a/code_test.py"}
	req.Components = []string{"a"}
	plan, err = repokit.PlanValidation(context.Background(), req)
	if err != nil || plan.Components[0].Checks[0].Scope != "full" {
		t.Fatalf("%+v %v", plan.Components, err)
	}
}

func TestValidationFallbackRootFullAndSkippedRequests(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo $(TEST_FILES) >> runs.out\n")
	fallbackChild(t, root, "child", "")
	// A missing root-owned file requires full root coverage even when the child falls back focused.
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, Files: []string{"missing.py", "child/code.py"}})
	if err != nil || len(plan.Components) != 1 || len(plan.Components[0].Checks) != 1 || plan.Components[0].Checks[0].Scope != "full" {
		t.Fatalf("full root must dominate: %+v %v", plan.Components, err)
	}
	// An entirely unaffected leaf remains skipped and must not wake root.
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	cmd = exec.Command("git", "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "initial")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	plan, err = repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"test"}, Base: "HEAD"})
	if err != nil || len(plan.Components) != 1 || plan.Components[0].Component.Root != "child" || plan.Components[0].Checks[0].Scope != "skipped" {
		t.Fatalf("%+v %v", plan.Components, err)
	}
}

func TestValidationFallbackMissingRootAndUnsafeMakefile(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@true\n")
	fallbackChild(t, root, "child", "")
	report, err := repokit.Test(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}})
	if err != nil || report.Passed || report.Components[0].Checks[0].Status != "unavailable" {
		t.Fatalf("%+v %v", report, err)
	}
	if err := os.Remove(filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("child/code.py", filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if _, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}}); err == nil {
		t.Fatal("root symlink accepted on fallback")
	}
}

func TestValidationFallbackPreservesSourceReceiptsAndReuseProhibition(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo changed > receipt.out\n")
	fallbackChild(t, root, "child", "")
	writeValidationFile(t, root, "receipt.out", "installed")
	req := repokit.ValidationRequest{HistoryDir: t.TempDir(), Repository: root, Components: []string{"child"}, Checks: []string{"test"}, Inputs: map[string]map[string]repokit.CheckInputDeclaration{"child": {"test": {Witnesses: []string{"receipt.out"}}}}}
	report, err := repokit.Validate(context.Background(), req)
	if err != nil || report.InputsConsistent || report.Passed {
		t.Fatalf("fallback lost receipt: %+v %v", report, err)
	}
	writeValidationFile(t, root, "Makefile", "test:\n\t@true\n")
	req.Inputs["child"]["test"] = repokit.CheckInputDeclaration{DisableReuse: true}
	report, err = repokit.Validate(context.Background(), req)
	if err != nil || !report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	report, err = repokit.Validate(context.Background(), req)
	if err != nil || !report.Passed || report.Components[0].Checks[0].Reused {
		t.Fatalf("%+v %v", report, err)
	}
	req.Inputs["child"]["test"] = repokit.CheckInputDeclaration{Components: []string{"missing"}}
	if _, err := repokit.PlanValidation(context.Background(), req); err == nil {
		t.Fatal("invalid source inputs ignored")
	}
}

func TestValidationFallbackDoesNotHideUnreadableLocalContract(t *testing.T) {
	root := validationRepo(t, "test:\n\t@true\n")
	fallbackChild(t, root, "child", strings.Repeat("# oversized contract\n", 120000))
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"test"}})
	if err != nil || len(plan.Components) != 1 || plan.Components[0].Component.Root != "child" || plan.Components[0].Checks[0].Scope != "unavailable" {
		t.Fatalf("%+v %v", plan.Components, err)
	}
}
