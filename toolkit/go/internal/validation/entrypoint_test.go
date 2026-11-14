package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func validationChild(t *testing.T, root, child, makefile string) {
	t.Helper()
	writeValidationFile(t, root, child+"/pyproject.toml", "[project]\nname='"+strings.ReplaceAll(child, "/", "-")+"'\nversion='0.1.0'\n")
	writeValidationFile(t, root, child+"/code.py", "pass\n")
	if makefile != "" {
		writeValidationFile(t, root, child+"/Makefile", makefile)
	}
}

// +case=Absent local targets skip independently, even when the root provides every target.
// +link=component://docs/validation.md
func TestValidationLocalTargetsOnly(t *testing.T) {
	root := validationRepo(t, "format lint test:\n\t@touch unexpected.out\n")
	validationChild(t, root, "child", "test-local:\n\t@echo child-test > child.out\n")
	writeValidationFile(t, root, "child/uv.lock", "")
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"format", "lint", "test"}, Inputs: map[string]map[string]repokit.CheckInputDeclaration{"child": {"lint": {Witnesses: []string{"absent-receipt"}}}}})
	if err != nil || !report.Passed || !report.InputsConsistent || len(report.Components) != 1 || len(report.Preparation) != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	if report.Preparation[0].Checks[0].Status != "skipped" || report.Components[0].Checks[0].Status != "skipped" || report.Components[0].Checks[1].Status != "passed" {
		t.Fatal(report)
	}
	if len(report.Components[0].Checks[0].DepotWitnesses) != 0 || len(report.Components[0].Checks[0].InputFiles) != 0 {
		t.Fatal("skipped target collected execution evidence")
	}
	if _, err := os.Stat(filepath.Join(root, "unexpected.out")); !os.IsNotExist(err) {
		t.Fatal("root ran", err)
	}
	if _, err := os.Stat(filepath.Join(root, "child/child.out")); err != nil {
		t.Fatal(err)
	}
}

func TestValidationDoesNotReadUnselectedRootMakefile(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@true\n")
	validationChild(t, root, "child", "")
	if err := os.Remove(filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("child/code.py", filepath.Join(root, "Makefile")); err != nil {
		t.Fatal(err)
	}
	report, err := repokit.Test(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}})
	if err != nil || !report.Passed || report.Components[0].Checks[0].Status != "skipped" {
		t.Fatalf("%+v %v", report, err)
	}
	if err := os.Symlink("code.py", filepath.Join(root, "child/Makefile")); err != nil {
		t.Fatal(err)
	}
	if _, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}}); err == nil {
		t.Fatal("local Makefile symlink accepted")
	}
}

func TestValidationUnreadableLocalContractIsNotSkipped(t *testing.T) {
	root := validationRepo(t, "test:\n\t@true\n")
	validationChild(t, root, "child", strings.Repeat("# oversized contract\n", 120000))
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"test"}})
	if err != nil || len(plan.Components) != 1 || plan.Components[0].Component.Root != "child" || plan.Components[0].Checks[0].Scope != "unavailable" {
		t.Fatalf("%+v %v", plan.Components, err)
	}
}
