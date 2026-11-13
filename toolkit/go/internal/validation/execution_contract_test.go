package validation_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=Native Go/Python execute the same selection with equivalent terminal evidence.
func TestSharedExecutionContract(t *testing.T) {
	data, err := os.ReadFile("../../../../conformance/validation/execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name, Makefile               string
		Reason, Error                string
		WithoutRoot                  bool
		Checks, Components, Selected []string
		Files                        map[string]string
		Expected                     map[string]any
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			root := validationRepo(t, row.Makefile)
			for name, text := range row.Files {
				writeValidationFile(t, root, name, text)
			}
			if row.WithoutRoot {
				for _, name := range []string{"Makefile", "pyproject.toml"} {
					if err := os.Remove(filepath.Join(root, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Checks: row.Checks, Components: row.Components, Files: row.Selected})
			if row.Error != "" {
				if err == nil || !strings.Contains(err.Error(), row.Error) {
					t.Fatalf("got %v, want %q", err, row.Error)
				}
				if _, err := os.Stat(filepath.Join(root, "runs.out")); !os.IsNotExist(err) {
					t.Fatal("invalid selection ran checks")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			checks := []map[string]any{}
			for _, group := range report.Components {
				for _, item := range group.Checks {
					if row.Reason != "" && item.Reason != row.Reason {
						t.Fatalf("reason = %q, want %q", item.Reason, row.Reason)
					}
					checks = append(checks, map[string]any{"component": group.Component.Root, "check": item.Check, "scope": item.Scope, "status": item.Status, "requestedComponents": item.RequestedComponents, "files": item.Files})
				}
			}
			encoded, _ := json.Marshal(map[string]any{"complete": report.Complete, "passed": report.Passed, "inputsConsistent": report.InputsConsistent, "checks": checks})
			var actual map[string]any
			if err := json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, row.Expected) {
				t.Fatalf("got %s want %+v", encoded, row.Expected)
			}
			if output, err := os.ReadFile(filepath.Join(root, "runs.out")); err == nil && strings.Count(string(output), "\n") != 1 {
				t.Fatalf("duplicate execution: %s", output)
			}
		})
	}
}
