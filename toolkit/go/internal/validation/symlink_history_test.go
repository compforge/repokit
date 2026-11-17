package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func replaceValidationLink(t *testing.T, root, name, target string) {
	t.Helper()
	name = filepath.Join(root, name)
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// +case=Link chains, linked VERSION contents, and VERSION link payloads remain part of reusable input identity across persisted history rounds.
func TestValidationLinkIdentity(t *testing.T) {
	for _, mode := range []string{"chain", "version target", "version link"} {
		t.Run(mode, func(t *testing.T) {
			alias, target := "alias", "first"
			if mode == "version target" {
				target = "VERSION"
			}
			if mode == "version link" {
				alias = "VERSION"
			}
			root := validationRepo(t, "test:\n\t@cat "+alias+" > result.out\n")
			writeValidationFile(t, root, target, "one\n")
			writeValidationFile(t, root, "second", "three\n")
			redirect := alias
			if mode == "chain" {
				replaceValidationLink(t, root, "middle", target)
				replaceValidationLink(t, root, alias, "middle")
				redirect = "middle"
			} else {
				replaceValidationLink(t, root, alias, target)
			}
			req := repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, HistoryDir: t.TempDir()}
			run := func(reused bool, want string) {
				t.Helper()
				report, err := repokit.Validate(context.Background(), req)
				if err != nil || !report.Passed || !report.Complete || !report.InputsConsistent {
					t.Fatalf("%+v %v", report, err)
				}
				check := report.Components[0].Checks[0]
				if check.Reused != reused || !strings.Contains(check.InputReason, "symlink targets") {
					t.Fatal(check)
				}
				actual, err := os.ReadFile(filepath.Join(root, "result.out"))
				if err != nil || string(actual) != want {
					t.Fatalf("got %q want %q: %v", actual, want, err)
				}
			}
			run(false, "one\n")
			run(true, "one\n")
			writeValidationFile(t, root, target, "two\n")
			run(false, "two\n")
			run(true, "two\n")
			replaceValidationLink(t, root, redirect, "second")
			run(false, "three\n")
			run(true, "three\n")
		})
	}
}

func TestValidationDirectoryLinkDoesNotGrantReuse(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo run >> runs.out\n")
	writeValidationFile(t, root, "data/file", "content")
	replaceValidationLink(t, root, "alias", "data")
	req := repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, HistoryDir: t.TempDir()}
	for range 2 {
		report, err := repokit.Validate(context.Background(), req)
		if err != nil || !report.Passed || !report.InputsConsistent {
			t.Fatalf("%+v %v", report, err)
		}
		check := report.Components[0].Checks[0]
		if check.Reused || check.ReuseReason != "symlink target is not a captured file: alias" {
			t.Fatal(check)
		}
	}
}

func TestValidationLinkTargetChangesInvalidateFrozenPlan(t *testing.T) {
	root := validationRepo(t, "test:\n\t@cat alias > result.out\n")
	writeValidationFile(t, root, "target", "before")
	replaceValidationLink(t, root, "alias", "target")
	plan, err := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"test"}})
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFile(t, root, "target", "after")
	report, err := repokit.ExecuteValidation(context.Background(), plan)
	if err != nil || report.Passed || report.InputsConsistent || len(report.Components) != 0 {
		t.Fatalf("changed target accepted: %+v %v", report, err)
	}
	if _, err = os.Stat(filepath.Join(root, "result.out")); !os.IsNotExist(err) {
		t.Fatal("stale plan executed", err)
	}
}
