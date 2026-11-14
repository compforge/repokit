package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=Component checks reuse across unrelated source and environment changes; explicit inputs still invalidate evidence.
// +link=component://docs/validation.md
func TestValidationDefaultComponentInputs(t *testing.T) {
	root := validationRepo(t, "test:\n\t@false\n")
	writeValidationFile(t, root, ".gitignore", "*.out\n.venv/\nnode_modules/\n")
	writeValidationFile(t, root, "go/go.mod", "module example.test/core\n\ngo 1.24\n")
	writeValidationFile(t, root, "go/Makefile", "lint:\n\t@echo run >> runs.out\n")
	writeValidationFile(t, root, "go/code.go", "package core\n")
	writeValidationFile(t, root, "go/nested/pyproject.toml", "[project]\nname='nested'\nversion='0.1.0'\n")
	writeValidationFile(t, root, "go/nested/code.py", "pass\n")
	writeValidationFile(t, root, "python/pyproject.toml", "[project]\nname='python'\nversion='0.1.0'\n")
	writeValidationFile(t, root, "python/uv.lock", "")
	writeValidationFile(t, root, "python/code.py", "pass\n")
	writeValidationFile(t, root, "web/package.json", "{}")
	req := repokit.ValidationRequest{Repository: root, Components: []string{"go"}, Checks: []string{"lint"}, HistoryDir: t.TempDir()}
	run := func(reused bool) repokit.CheckResult {
		t.Helper()
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || !report.InputsConsistent || len(report.Components) != 1 {
			t.Fatalf("%+v %v", report, err)
		}
		check := report.Components[0].Checks[0]
		if check.Reused != reused {
			t.Fatalf("reused=%t, want %t: %+v", check.Reused, reused, check)
		}
		return check
	}
	first := run(false)
	if len(first.DepotWitnesses) != 0 || slices.Contains(first.InputFiles, "python/code.py") || slices.Contains(first.InputFiles, "go/nested/code.py") {
		t.Fatalf("unrelated inputs: %+v", first)
	}
	writeValidationFile(t, root, "python/code.py", "# unrelated\n")
	writeValidationFile(t, root, "go/nested/code.py", "# independent nested Component\n")
	writeValidationFile(t, root, "web/node_modules/installed", "present")
	run(true)
	if err := os.RemoveAll(filepath.Join(root, "web/node_modules")); err != nil {
		t.Fatal(err)
	}
	run(true)
	writeValidationFile(t, root, "go/new.go", "package core\n")
	run(false)
	if err := os.Remove(filepath.Join(root, "go/new.go")); err != nil {
		t.Fatal(err)
	}
	run(true) // The earlier exact successful input remains reusable.
	req.Inputs = map[string]map[string]repokit.CheckInputDeclaration{"go": {"lint": {Paths: []string{"python/code.py"}}}}
	check := run(false)
	if len(check.DepotWitnesses) != 0 {
		t.Fatal("a shared file imported its owner's environment")
	}
	run(true)
	writeValidationFile(t, root, "python/code.py", "# shared fixture changed\n")
	run(false)
	req.Inputs["go"]["lint"] = repokit.CheckInputDeclaration{Components: []string{"python"}}
	check = run(false)
	if check.DepotWitnesses["python/.venv"] != "missing" {
		t.Fatal("explicit Component dependency omitted", check)
	}
	run(false)
	writeValidationFile(t, root, "python/.venv/.repokit-dependencies.json", `{"manager":"uv","fingerprint":"one"}`)
	run(false)
	run(true)
	writeValidationFile(t, root, "python/code.py", "# declared dependency changed\n")
	run(false)
	writeValidationFile(t, root, "python/.venv/.repokit-dependencies.json", `{"manager":"uv","fingerprint":"two"}`)
	run(false)
}

func TestValidationSymlinkDoesNotWidenComponentInputs(t *testing.T) {
	root := validationRepo(t, "test:\n\t@true\n")
	validationChild(t, root, "child", "test:\n\t@true\n")
	writeValidationFile(t, root, "shared.py", "pass\n")
	if err := os.Symlink("../shared.py", filepath.Join(root, "child/linked.py")); err != nil {
		t.Fatal(err)
	}
	req := repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"test"}, HistoryDir: t.TempDir()}
	for range 2 {
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed {
			t.Fatalf("%+v %v", report, err)
		}
		check := report.Components[0].Checks[0]
		if check.Reused || check.ReuseReason != "symlink input content is not captured" || slices.Contains(check.InputFiles, "shared.py") {
			t.Fatal(check)
		}
	}
}
