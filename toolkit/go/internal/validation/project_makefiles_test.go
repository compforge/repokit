package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=RepoKit's actual Makefiles select package directories, deduplicate Go files, preserve Node-only selection, and keep the full-check contract for empty or unknown selections.
func TestProjectMakefileTestSelection(t *testing.T) {
	for _, tc := range []struct {
		name, makefile string
		files, want    []string
	}{
		{"go packages", "../../Makefile", []string{"alpha/a_test.go", "alpha/b_test.go", "beta/c_test.go"}, []string{"go test -json ./alpha/ ./beta/"}},
		{"go full", "../../Makefile", nil, []string{"go test -json ./..."}},
		{"go unknown", "../../Makefile", []string{"config.json"}, []string{"go test -json ./..."}},
		{"cli go only", "../../../../apps/cli/Makefile", []string{"alpha/a_test.go"}, []string{"go test -json ./alpha/"}},
		{"cli node only", "../../../../apps/cli/Makefile", []string{"internal/viewer/a_test.cjs"}, []string{"node --test internal/viewer/a_test.cjs"}},
		{"cli mixed", "../../../../apps/cli/Makefile", []string{"alpha/a_test.go", "internal/viewer/a_test.cjs"}, []string{"node --test internal/viewer/a_test.cjs", "go test -json ./alpha/"}},
		{"cli full", "../../../../apps/cli/Makefile", nil, []string{"node --test internal/viewer/a_test.cjs", "go test -json ./..."}},
		{"cli unknown", "../../../../apps/cli/Makefile", []string{"config.json"}, []string{"node --test internal/viewer/a_test.cjs", "go test -json ./..."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(tc.makefile)
			if err != nil {
				t.Fatal(err)
			}
			root := validationRepo(t, string(source))
			for _, file := range []string{"alpha/a_test.go", "alpha/b_test.go", "beta/c_test.go", "internal/viewer/a_test.cjs", "config.json"} {
				writeValidationFile(t, root, file, "fixture\n")
			}
			bin := t.TempDir()
			for _, tool := range []string{"go", "node"} {
				script := "#!/bin/sh\nprintf '%s' '" + tool + "'\nfor arg do printf ' %s' \"$arg\"; done\nprintf '\\n'\n"
				if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			environment := []string{}
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "PATH=") {
					environment = append(environment, entry)
				}
			}
			environment = append(environment, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			req := repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, Files: tc.files, Environment: environment}
			report, err := repokit.Validate(context.Background(), req)
			if err != nil || !report.Passed || !report.InputsConsistent {
				t.Fatal(report, err)
			}
			check := report.Components[0].Checks[0]
			if len(tc.files) > 0 && check.Scope != "focused" {
				t.Fatal(check)
			}
			for _, line := range tc.want {
				if !strings.Contains(check.Stdout, line+"\n") {
					t.Fatalf("want %q in %q", line, check.Stdout)
				}
			}
			if !strings.Contains(strings.Join(tc.want, "\n"), "node --test") && strings.Contains(check.Stdout, "node --test") {
				t.Fatal(check.Stdout)
			}
			if !strings.Contains(strings.Join(tc.want, "\n"), "go test") && strings.Contains(check.Stdout, "go test") {
				t.Fatal(check.Stdout)
			}
		})
	}
}
