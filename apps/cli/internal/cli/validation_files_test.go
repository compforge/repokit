package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

// +case=Explicit CLI files select their owner through format and validation; a conflicting Component boundary fails before checks run.
// +link=component://docs/validation.md
func TestValidationCLIFileOwners(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	put(t, root, "selected.py", "# selected\n")
	put(t, root, "Makefile", "format lint test:\n\t@echo $(LINT_FILES) $(TEST_FILES)\n")
	put(t, root, "child/pyproject.toml", "[project]\nname='child'\nversion='0.1.0'\n")
	put(t, root, "child/Makefile", "format lint test:\n\t@false\n")
	args := []string{"validate", "--repo", root, "--home", home, "--file", "selected.py", "--check", "format,lint,test", "--json"}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 {
		t.Fatalf("exit=%d %s %s", code, &out, &stderr)
	}
	var report repokit.ValidationReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Passed || !report.InputsConsistent || len(report.Components) != 1 || report.Components[0].Component.Root != "." || len(report.Preparation) != 1 || report.Preparation[0].Component.Root != "." {
		t.Fatalf("unexpected file selection: %s", &out)
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), append(args, "--component", "child"), nil, &out, &stderr); code == 0 || !strings.Contains(out.String(), "outside selected Components") {
		t.Fatalf("conflicting boundary accepted: exit=%d %s %s", code, &out, &stderr)
	}
}

// +case=Automatic scope includes branch and worktree changes, keeps check-specific Makefile contracts and falls back when Git cannot establish the comparison.
func TestValidationCLIAutomaticScope(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	put(t, root, "Makefile", "lint:\n\t@true\ntest:\n\t@echo $(TEST_FILES)\n")
	gitCommand(t, root, "add", "Makefile")
	gitCommand(t, root, "commit", "-qm", "test recipe")
	checks := func(extra ...string) []repokit.CheckSpec {
		t.Helper()
		var out, stderr bytes.Buffer
		args := append([]string{"validate", "--repo", root, "--component", ".", "--home", home, "--plan", "--json"}, extra...)
		if code := Execute(t.Context(), args, nil, &out, &stderr); code != 0 {
			t.Fatalf("exit %d: %s %s", code, &out, &stderr)
		}
		var plan repokit.ValidationPlan
		if err := json.Unmarshal(resultData(t, out.Bytes()), &plan); err != nil {
			t.Fatal(err)
		}
		return plan.Components[0].Checks
	}
	got := checks()
	if got[1].Scope != "full" || !strings.Contains(got[1].Reason, "baseline unavailable") {
		t.Fatalf("unknown baseline: %+v", got[1])
	}
	gitCommand(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitCommand(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gitCommand(t, root, "switch", "-qc", "topic")
	put(t, root, "README.md", "documentation only\n")
	got = checks()
	if got[0].Scope != "skipped" || got[1].Scope != "skipped" {
		t.Fatalf("documentation: %+v", got)
	}
	put(t, root, "first_test.go", "package main\nimport \"testing\"\nfunc TestFirst(t *testing.T){}\n")
	gitCommand(t, root, "add", "first_test.go")
	gitCommand(t, root, "commit", "-qm", "first test")
	put(t, root, "next_test.go", "package main\nimport \"testing\"\nfunc TestNext(t *testing.T){}\n")
	got = checks()
	if got[0].Scope != "full" || got[1].Scope != "focused" || !slices.Equal(got[1].Files, []string{"first_test.go", "next_test.go"}) {
		t.Fatalf("branch plus working tree: %+v", got)
	}
	if got := checks("--full"); got[1].Scope != "full" || len(got[1].Files) != 0 {
		t.Fatalf("explicit full: %+v", got)
	}
	put(t, root, "Makefile", "lint:\n\t@true\ntest:\n\t@echo running $(TEST_FILES)\n")
	if got := checks(); got[1].Scope != "full" || !strings.Contains(got[1].Reason, "incomplete impact") {
		t.Fatalf("configuration change: %+v", got)
	}
}
