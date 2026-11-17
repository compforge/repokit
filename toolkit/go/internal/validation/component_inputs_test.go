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

// +case=A cross-Component file alias imports only the linked content, reuses unchanged checks, and invalidates when that content changes.
func TestValidationSymlinkFileInputs(t *testing.T) {
	root := validationRepo(t, "test:\n\t@true\n")
	validationChild(t, root, "child", "test:\n\t@cat linked.py > result.out\n")
	writeValidationFile(t, root, "shared.py", "one\n")
	writeValidationFile(t, root, "unrelated.py", "unrelated\n")
	if err := os.Symlink("../shared.py", filepath.Join(root, "child/linked.py")); err != nil {
		t.Fatal(err)
	}
	req := repokit.ValidationRequest{Repository: root, Components: []string{"child"}, Checks: []string{"test"}, HistoryDir: t.TempDir()}
	run := func(wantReuse bool, want string) {
		t.Helper()
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || !report.InputsConsistent {
			t.Fatalf("%+v %v", report, err)
		}
		check := report.Components[0].Checks[0]
		if check.Reused != wantReuse || !slices.Contains(check.InputFiles, "shared.py") || slices.Contains(check.InputFiles, "unrelated.py") || len(check.DepotWitnesses) != 0 {
			t.Fatal(check)
		}
		got, err := os.ReadFile(filepath.Join(root, "child/result.out"))
		if err != nil || string(got) != want {
			t.Fatalf("%q %v", got, err)
		}
	}
	run(false, "one\n")
	run(true, "one\n")
	writeValidationFile(t, root, "unrelated.py", "unrelated edit\n")
	run(true, "one\n")
	writeValidationFile(t, root, "shared.py", "two\n")
	run(false, "two\n")
	run(true, "two\n")
}
