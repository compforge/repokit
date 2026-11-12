package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=A failed historical check is a successful query; unavailable artifacts are partial, unknown IDs fail, and neither renews state or writes command logs.
func TestValidationHistoryCLI(t *testing.T) {
	root := fixture(t)
	put(t, root, "Makefile", "test:\n\t@echo historical-output\n\t@echo historical-error >&2\n\t@false\n")
	dir := t.TempDir()
	report, err := repokit.Validate(context.Background(), repokit.ValidationRequest{Repository: root, HistoryDir: dir, Checks: []string{"test"}})
	if err != nil || report.Passed || len(report.Components) != 1 {
		t.Fatal(report, err)
	}
	id := report.Components[0].Checks[0].RunID
	home := filepath.Join(t.TempDir(), "unused-home")
	run := func(args ...string) (int, commandResult) {
		t.Helper()
		var out, stderr bytes.Buffer
		command := append([]string{"validate", "history"}, args...)
		command = append(command, "--repo", root, "--history-dir", dir, "--home", home, "--json")
		code := Execute(context.Background(), command, nil, &out, &stderr)
		var result commandResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err, out.String(), stderr.String())
		}
		return code, result
	}
	code, list := run("list", "--status", "failed", "--check", "test", "--limit", "1")
	if code != 0 || list.Command != "repocli validate history list" || list.Outcome != "succeeded" || list.Cost.Validation != nil {
		t.Fatal(code, list)
	}
	code, shown := run("show", id)
	if code != 0 || shown.Outcome != "succeeded" || shown.Cost.Validation != nil {
		t.Fatal(code, shown)
	}
	encoded, _ := json.Marshal(shown.Data)
	var detail repokit.ValidationRunDetail
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != "failed" || !detail.Complete || !strings.Contains(detail.Result.Stdout, "historical-output") || detail.Inputs == nil {
		t.Fatal(detail)
	}
	var text, stderr bytes.Buffer
	code = Execute(context.Background(), []string{"validate", "history", "show", id, "--repo", root, "--history-dir", dir, "--home", home}, nil, &text, &stderr)
	if code != 0 || !strings.Contains(text.String(), "historical-error") || !strings.Contains(text.String(), "No checks executed") || !strings.Contains(text.String(), "historicalExecution=") {
		t.Fatal(code, text.String(), stderr.String())
	}
	exportPath := filepath.Join(t.TempDir(), "detail.json")
	code, exported := run("show", id, "--output", exportPath)
	saved, err := os.ReadFile(exportPath)
	var decoded commandResult
	if err != nil || json.Unmarshal(saved, &decoded) != nil || code != 0 || !reflect.DeepEqual(exported, decoded) || len(decoded.Artifacts) != 1 {
		t.Fatal(code, exported, err)
	}
	entries, _ := os.ReadDir(dir)
	if err := os.RemoveAll(filepath.Join(dir, entries[0].Name(), "logs")); err != nil {
		t.Fatal(err)
	}
	code, partial := run("show", id)
	if code != 1 || partial.Outcome != "partial" || partial.Data == nil || len(partial.Diagnostics) == 0 || partial.Diagnostics[0].Evidence != "/data/diagnostics/0" {
		t.Fatal(code, partial)
	}
	code, missing := run("show", rand.Text())
	if code != 1 || missing.Outcome != "failed" || missing.Error == nil || missing.Error.Code != "validation.run_not_found" {
		t.Fatal(code, missing)
	}
	code, invalid := run("list", "--limit", "0")
	if code != 2 || invalid.Outcome != "failed" {
		t.Fatal(code, invalid)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("history query created user state or logs", err)
	}
}
