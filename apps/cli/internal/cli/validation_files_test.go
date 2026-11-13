package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
