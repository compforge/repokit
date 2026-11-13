package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/state"
)

// +case=Validation commands renew checkout access without refreshing topology, even with a corrupt unrelated branch projection.
// +link=component://docs/state.md
func TestValidationCLILeavesTopologyUntouched(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, "Makefile", "format lint test:\n\t@true\n")
	ctx := context.Background()
	directory, err := state.Observe(ctx, root, home, false)
	if err != nil {
		t.Fatal(err)
	}
	branch := filepath.Join(directory, "branches", "main", "observation.json")
	before, err := os.ReadFile(branch)
	if err != nil {
		t.Fatal(err)
	}
	put(t, directory, "branches/unrelated/observation.json", "broken")
	for _, name := range []string{"format", "lint", "test", "validate"} {
		t.Run(name, func(t *testing.T) {
			activity := filepath.Join(directory, "activity.json")
			if err := os.Remove(activity); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := Execute(ctx, []string{name, "--repo", root, "--home", home, "--json"}, nil, &out, &stderr); code != 0 {
				t.Fatalf("exit=%d %s %s", code, &out, &stderr)
			}
			var result commandResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == "state.observation_unavailable" {
					t.Fatalf("validation observed unrelated topology: %s", &out)
				}
			}
			for _, phase := range result.Cost.Phases {
				if phase.Name == "observation" {
					t.Fatalf("validation performed topology observation: %s", &out)
				}
			}
			after, err := os.ReadFile(branch)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("validation changed branch observation: %v", err)
			}
			data, err := os.ReadFile(activity)
			if err != nil {
				t.Fatal(err)
			}
			var access struct {
				Checkouts map[string]struct {
					Path         string  `json:"path"`
					LastAccessAt float64 `json:"lastAccessAt"`
				} `json:"checkouts"`
			}
			if err := json.Unmarshal(data, &access); err != nil {
				t.Fatal(err)
			}
			if len(access.Checkouts) != 1 {
				t.Fatalf("unexpected checkout access: %s", data)
			}
			for _, checkout := range access.Checkouts {
				if checkout.Path != root || checkout.LastAccessAt <= 0 {
					t.Fatalf("wrong checkout access: %s", data)
				}
			}
		})
	}
}

// +case=Fresh checkouts retain successful, reused and failed validation evidence; branch queries establish topology on demand.
func TestValidationCLIHistoryWithoutTopology(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	put(t, root, "Makefile", "test:\n\t@echo checked\n")
	ctx := context.Background()
	history := filepath.Join(home, "repositories")
	var original string
	for i := range 3 {
		wantStatus, wantCode := "passed", 0
		if i == 2 {
			put(t, root, "Makefile", "test:\n\t@echo failed; exit 1\n")
			wantStatus, wantCode = "failed", 1
		}
		var out, stderr bytes.Buffer
		if code := Execute(ctx, []string{"test", "--repo", root, "--home", home, "--json"}, nil, &out, &stderr); code != wantCode {
			t.Fatalf("exit=%d %s %s", code, &out, &stderr)
		}
		var report repokit.ValidationReport
		if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
			t.Fatal(err)
		}
		check := report.Components[0].Checks[0]
		if check.Status != wantStatus || !report.InputsConsistent || check.Reused != (i == 1) {
			t.Fatalf("unexpected check: %+v", check)
		}
		if i == 0 {
			original = check.RunID
		} else if i == 1 && check.RunID != original {
			t.Fatal("reuse replaced the original run")
		}
		detail, err := repokit.ReadValidationRun(ctx, history, root, check.RunID)
		if err != nil || !detail.Complete || detail.Run.Status != wantStatus {
			t.Fatalf("missing retained evidence: %+v %v", detail, err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(history, "*", "branches", "*", "observation.json"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("validation published topology: %v %v", paths, err)
	}
	branch, err := state.LoadBranch(ctx, root, "main", home, 0)
	if err != nil || branch == nil || !branch.Complete || branch.Source != "git" || len(branch.Checkouts) != 1 {
		t.Fatalf("on-demand topology unavailable: %+v %v", branch, err)
	}
}
