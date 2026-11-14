package validation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

func writeValidationFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestValidationComponentReuseAndWitnesses(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@true\n")
	for _, name := range []string{"a", "b"} {
		writeValidationFile(t, root, name+"/pyproject.toml", "[project]\nname = '"+name+"'\nversion = '0.1.0'\n")
		writeValidationFile(t, root, name+"/Makefile", "test:\n\t@echo executed >> runs.out\n")
		writeValidationFile(t, root, name+"/code.py", "pass\n")
	}
	writeValidationFile(t, root, "shared.cfg", "one")
	writeValidationFile(t, root, "receipt.out", "installed")
	req := repokit.ValidationRequest{HistoryDir: t.TempDir(), Repository: root, Components: []string{"a"}, Checks: []string{"test"}, Inputs: map[string]map[string]repokit.CheckInputDeclaration{"a": {"test": {Components: []string{"a"}, Paths: []string{"shared.cfg"}, Witnesses: []string{"receipt.out"}}}}}
	first, err := repokit.Validate(context.Background(), req)
	if err != nil || !first.Passed {
		t.Fatalf("%+v %v", first, err)
	}
	writeValidationFile(t, root, "b/code.py", "# unrelated edit\n")
	second, err := repokit.Validate(context.Background(), req)
	if err != nil || !second.Passed || !second.Components[0].Checks[0].Reused || second.Snapshot == first.Snapshot {
		t.Fatalf("%+v %v", second, err)
	}
	if second.Components[0].Checks[0].CheckedAt != first.Components[0].Checks[0].CheckedAt {
		t.Fatal("reuse changed actual execution timestamp")
	}
	for _, name := range []string{"shared.cfg", "receipt.out", "a/new.py"} {
		writeValidationFile(t, root, name, "changed")
		second, err = repokit.Validate(context.Background(), req)
		if err != nil || !second.Passed || second.Components[0].Checks[0].Reused {
			t.Fatalf("%s: %+v %v", name, second, err)
		}
	}
}

func TestValidationFailedAndDisabledEvidenceNeverReuse(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo run >> runs.out\n\t@false\n")
	req := repokit.ValidationRequest{HistoryDir: t.TempDir(), Repository: root, Checks: []string{"test"}}
	first, err := repokit.Validate(context.Background(), req)
	if err != nil || first.Passed || !first.InputsConsistent {
		t.Fatalf("%+v %v", first, err)
	}
	again, err := repokit.Validate(context.Background(), req)
	if err != nil || again.Components[0].Checks[0].Reused {
		t.Fatalf("%+v %v", again, err)
	}
	writeValidationFile(t, root, "Makefile", "test:\n\t@true\n")
	req.Inputs = map[string]map[string]repokit.CheckInputDeclaration{".": {"test": {DisableReuse: true}}}
	first, err = repokit.Validate(context.Background(), req)
	if err != nil || !first.Passed {
		t.Fatal(err)
	}
	again, err = repokit.Validate(context.Background(), req)
	if err != nil || again.Components[0].Checks[0].Reused {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestValidationReceiptMutationInvalidatesRound(t *testing.T) {
	root := validationRepo(t, "test:\n\t@echo changed > receipt.out\n")
	writeValidationFile(t, root, "receipt.out", "original")
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, Inputs: map[string]map[string]repokit.CheckInputDeclaration{".": {"test": {Witnesses: []string{"receipt.out"}}}}})
	if err != nil || report.InputsConsistent || report.Passed || report.Components[0].Checks[0].Status != "passed" {
		t.Fatalf("%+v %v", report, err)
	}
}

// +case:id=format_target_aliases,desc=`Format, fmt and fix share formatting semantics`,expect=`Each alias runs before replanned checks and never grants check evidence by itself`
func TestValidationFormatReplansAndHasNoCheckEvidence(t *testing.T) {
	for _, target := range []string{"format", "fmt", "fix"} {
		t.Run(target, func(t *testing.T) {
			root := validationRepo(t, target+":\n\t@echo formatted > source.py\nlint:\n\t@grep -q formatted source.py\ntest:\n\t@grep -q formatted source.py\n")
			writeValidationFile(t, root, "source.py", "unformatted")
			formatted, err := repokit.Format(context.Background(), repokit.ValidationRequest{Repository: root})
			if err != nil || !formatted.Passed || len(formatted.Components) != 0 || formatted.InputsConsistent {
				t.Fatalf("%+v %v", formatted, err)
			}
			writeValidationFile(t, root, "source.py", "unformatted again")
			report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"test", "format", "lint"}})
			if err != nil || !report.Passed || !report.InputsConsistent || len(report.Preparation) != 1 || len(report.Components[0].Checks) != 2 {
				t.Fatalf("%+v %v", report, err)
			}
			plan := validationPlan(t, root, "format")
			if _, err := repokit.ExecuteValidation(context.Background(), plan); err == nil {
				t.Fatal("format entered readonly evidence executor")
			}
		})
	}
}

func TestValidationMissingTargetDoesNotInvalidateSuccessfulCheck(t *testing.T) {
	root := validationRepo(t, "lint:\n\t@true\n")
	history := t.TempDir()
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, HistoryDir: history})
	if err != nil || !report.InputsConsistent || !report.Complete || !report.Passed || report.Components[0].Checks[0].Status != "passed" {
		t.Fatalf("%+v %v", report, err)
	}
	req := repokit.ValidationRequest{HistoryDir: history, Repository: root, Checks: []string{"lint"}}
	again, err := repokit.Validate(context.Background(), req)
	if err != nil || !again.Passed || !again.Components[0].Checks[0].Reused {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestValidationBoundedConcurrency(t *testing.T) {
	root := validationRepo(t, "lint test:\n\t@python3 counter.py\n")
	writeValidationFile(t, root, "counter.py", `import fcntl, json, time
from pathlib import Path

def change(delta):
    with open('lock.out', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        path = Path('count.out')
        active, peak = json.loads(path.read_text()) if path.exists() else [0, 0]
        active += delta
        path.write_text(json.dumps([active, max(peak, active)]))
change(1)
time.sleep(0.2)
change(-1)
`)
	for _, budget := range []int{1, 2} {
		os.Remove(filepath.Join(root, "count.out"))
		report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Concurrency: budget})
		if err != nil || !report.Passed {
			t.Fatalf("%+v %v", report, err)
		}
		value, err := os.ReadFile(filepath.Join(root, "count.out"))
		expected := "[0, 1]"
		if budget == 2 {
			expected = "[0, 2]"
		}
		if err != nil || strings.TrimSpace(string(value)) != expected {
			t.Fatalf("budget %d: %s %v", budget, value, err)
		}
	}
}

func TestValidationFormatCreatedComponentJoinsImpactSeeds(t *testing.T) {
	root := validationRepo(t, "format:\n\t@mkdir -p child\n\t@printf 'module example.com/child\\n' > child/go.mod\n\t@printf 'lint test:\\n\\t@touch ran.out\\n' > child/Makefile\nlint test:\n\t@true\n")
	writeValidationFile(t, root, "source.py", "before")
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, output)
		}
	}
	writeValidationFile(t, root, "source.py", "after")
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Base: "HEAD", ChangedFiles: []string{"source.py"}, Checks: []string{"format", "lint", "test"}})
	if err != nil || !report.Passed {
		t.Fatalf("%+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(root, "child/ran.out")); err != nil {
		t.Fatal("generated child was not checked", err)
	}
	if report.Impact == nil {
		t.Fatal("original impact evidence omitted")
	}
}

func TestValidationFormatSupportsExplicitFiles(t *testing.T) {
	root := validationRepo(t, "format:\n\t@printf '%s' '$(LINT_FILES)' > files.out\n")
	writeValidationFile(t, root, "source.py", "pass")
	report, err := repokit.Format(context.Background(), repokit.ValidationRequest{Repository: root, Files: []string{"source.py"}})
	if err != nil || !report.Passed || report.Preparation[0].Checks[0].Scope != "focused" {
		t.Fatalf("%+v %v", report, err)
	}
	files, err := os.ReadFile(filepath.Join(root, "files.out"))
	if err != nil || string(files) != "source.py" {
		t.Fatalf("%s %v", files, err)
	}
}

// +case:id=format_target_precedence,desc=`Legacy Makefile aliases do not cause multiple format runs`,expect=`Format then fmt then fix priority selects one command while reports keep the format operation`
func TestValidationFormatTargetPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []string
		want    string
	}{
		{"all", []string{"fix", "fmt", "format"}, "format"},
		{"legacy", []string{"fix", "fmt"}, "fmt"},
		{"fix", []string{"fix"}, "fix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var makefile strings.Builder
			for _, target := range tc.targets {
				makefile.WriteString(target + ":\n\t@echo " + target + " >> runs.out\n")
			}
			root := validationRepo(t, makefile.String())
			plan := validationPlan(t, root, "format")
			check := plan.Components[0].Checks[0]
			if check.Check != "format" || check.Command[3] != tc.want {
				t.Fatalf("selected check: %+v", check)
			}
			report, err := repokit.Format(context.Background(), repokit.ValidationRequest{Repository: root})
			if err != nil || !report.Passed || len(report.Preparation) != 1 || len(report.Components) != 0 || report.InputsConsistent {
				t.Fatalf("%+v %v", report, err)
			}
			result := report.Preparation[0].Checks[0]
			if result.Check != "format" || result.Command[3] != tc.want {
				t.Fatalf("reported check: %+v", result)
			}
			runs, err := os.ReadFile(filepath.Join(root, "runs.out"))
			if err != nil || string(runs) != tc.want+"\n" {
				t.Fatalf("runs = %q: %v", runs, err)
			}
		})
	}
}

func TestValidationFailedFormatDoesNotTryAliases(t *testing.T) {
	for _, target := range []string{"format", "fmt"} {
		t.Run(target, func(t *testing.T) {
			root := validationRepo(t, target+":\n\t@false\nfix lint:\n\t@touch unexpected.out\n")
			report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, Checks: []string{"format", "lint"}})
			if err != nil || report.Passed || len(report.Components) != 0 || len(report.Preparation) != 1 || report.Preparation[0].Checks[0].Status != "failed" {
				t.Fatalf("%+v %v", report, err)
			}
			if _, err := os.Stat(filepath.Join(root, "unexpected.out")); !os.IsNotExist(err) {
				t.Fatalf("fallback alias or lint ran after failed format: %v", err)
			}
		})
	}
}
