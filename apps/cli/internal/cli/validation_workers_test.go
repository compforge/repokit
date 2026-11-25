package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func TestValidationCLIWorkers(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	put(t, root, "Makefile", "LINT_WORKERS := 1\nTEST_WORKERS := 1\nformat lint test:\n\t@printf '%s|%s' '$(LINT_WORKERS)' '$(TEST_WORKERS)'\n")
	for _, command := range []string{"validate", "lint", "test"} {
		t.Run(command, func(t *testing.T) {
			args := []string{command, "--repo", root, "--home", home, "--json"}
			if command != "test" {
				args = append(args, "--lint-workers", "3")
			}
			if command != "lint" {
				args = append(args, "--test-workers", "4")
			}
			if command == "validate" {
				args = append(args, "--check", "format,lint,test")
			}
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 {
				t.Fatalf("exit=%d %s %s", code, &out, &stderr)
			}
			var report repokit.ValidationReport
			if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
				t.Fatal(err)
			}
			if !report.Passed || !report.InputsConsistent {
				t.Fatalf("%s", &out)
			}
			for _, check := range report.Components[0].Checks {
				want := "3|1"
				if check.Check == "test" {
					want = "1|4"
				}
				if check.Stdout != want {
					t.Fatalf("%+v", check)
				}
			}
			if command == "validate" && report.Preparation[0].Checks[0].Stdout != "1|1" {
				t.Fatalf("format received worker overrides: %+v", report.Preparation)
			}
		})
	}
}

func TestValidationCLIRejectsInvalidOptions(t *testing.T) {
	for _, flags := range [][]string{
		{"--concurrency", "1"},
		{"--lint-workers", "0"}, {"--lint-workers", "-1"}, {"--test-workers", "0"}, {"--test-workers", "-1"},
		{"--format-workers", "0"}, {"--build-workers", "-1"}, {"--history", "--build-workers", "2"},
		{"--history", "--lint-workers", "2"}, {"--history", "--test-workers", "2"},
	} {
		var out, stderr bytes.Buffer
		args := append([]string{"validate", "--repo", t.TempDir(), "--json"}, flags...)
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 2 || !strings.Contains(out.String(), "usage.invalid_argument") {
			t.Fatalf("flags=%v exit=%d %s %s", flags, code, &out, &stderr)
		}
	}
}
