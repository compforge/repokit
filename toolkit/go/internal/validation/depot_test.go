package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case:id=reclaimed_dependency_reuse,desc=`A successful check loses its ignored dependency Bin`,expect=`Real execution fails instead of reusing the prior pass`
func TestValidationDepotReclamation(t *testing.T) {
	for _, fixture := range []struct{ name, directory, manifest, content, manager string }{
		{"node", "node_modules", "package.json", "{}", "npm"},
		{"uv", ".venv", "pyproject.toml", "[project]", "uv"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			directory := fixture.directory
			root := validationRepo(t, "test:\n\t@test -f "+directory+"/installed\n")
			writeValidationFile(t, root, fixture.manifest, fixture.content)
			if fixture.name == "uv" {
				writeValidationFile(t, root, "uv.lock", "")
			}
			writeValidationFile(t, root, ".gitignore", directory+"/\ndist/\n*.out\n")
			writeValidationFile(t, root, directory+"/installed", "ready")
			receipt := `{"manager":"` + fixture.manager + `","fingerprint":"fixture"}`
			writeValidationFile(t, root, directory+"/.repokit-dependencies.json", receipt)
			request := repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, HistoryDir: t.TempDir()}
			run := func() repokit.ValidationReport {
				t.Helper()
				result, err := repokit.Validate(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			if result := run(); !result.Passed || result.Components[0].Checks[0].Reused {
				t.Fatal(result)
			}
			writeValidationFile(t, root, "dist/generated.js", "output changes do not invalidate dependencies")
			if result := run(); !result.Passed || !result.Components[0].Checks[0].Reused {
				t.Fatal(result)
			}
			if err := os.RemoveAll(filepath.Join(root, directory)); err != nil {
				t.Fatal(err)
			}
			result := run()
			if result.Passed || result.Components[0].Checks[0].Reused || !result.InputsConsistent {
				t.Fatal(result)
			}
			if _, err := os.Stat(filepath.Join(root, directory)); !os.IsNotExist(err) {
				t.Fatal("validation implicitly installed dependencies", err)
			}
			writeValidationFile(t, root, directory+"/installed", "restored without receipt")
			if result := run(); !result.Passed || result.Components[0].Checks[0].Reused {
				t.Fatal(result)
			}
			if result := run(); !result.Passed || result.Components[0].Checks[0].Reused {
				t.Fatal("unmanaged Bin reused", result)
			}
		})
	}
}

func TestValidationSharedDepotAndFrozenPlan(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@true\n")
	writeValidationFile(t, root, "package.json", `{"workspaces":["packages/*"]}`)
	writeValidationFile(t, root, "package-lock.json", "one")
	writeValidationFile(t, root, ".gitignore", "node_modules/\n*.out\n")
	writeValidationFile(t, root, "packages/app/package.json", `{}`)
	writeValidationFile(t, root, "packages/app/Makefile", "test:\n\t@echo run >> runs.out\n")
	writeValidationFile(t, root, "packages/other/package.json", `{}`)
	writeValidationFile(t, root, "packages/other/code.js", "original")
	writeValidationFile(t, root, "node_modules/.repokit-dependencies.json", `{"manager":"npm","fingerprint":"fixture"}`)
	req := repokit.ValidationRequest{Repository: root, Components: []string{"packages/app"}, Checks: []string{"test"}, HistoryDir: t.TempDir()}
	for i := 0; i < 2; i++ {
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || report.Components[0].Checks[0].Reused != (i == 1) {
			t.Fatalf("%+v %v", report, err)
		}
	}
	writeValidationFile(t, root, "packages/other/code.js", "unrelated source changed")
	report, err := repokit.Validate(context.Background(), req)
	if err != nil || !report.Passed || !report.Components[0].Checks[0].Reused {
		t.Fatalf("shared Bin included unrelated source: %+v %v", report, err)
	}
	for _, name := range []string{"packages/other/package.json", ".npmrc"} {
		writeValidationFile(t, root, name, `{"name":"changed"}`)
		report, err = repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || report.Components[0].Checks[0].Reused {
			t.Fatalf("installation input %s did not invalidate: %+v %v", name, report, err)
		}
	}
	writeValidationFile(t, root, "package-lock.json", "two")
	report, err = repokit.Validate(context.Background(), req)
	if err != nil || !report.Passed || report.Components[0].Checks[0].Reused {
		t.Fatalf("shared lock edit reused: %+v %v", report, err)
	}
	plan, err := repokit.PlanValidation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	report, err = repokit.ExecuteValidation(context.Background(), plan)
	if err != nil || report.Passed || report.InputsConsistent || len(report.Components) > 0 {
		t.Fatalf("stale plan ran: %+v %v", report, err)
	}
}
