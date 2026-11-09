package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/apps/cli/internal/stats"
	"github.com/compforge/repokit/toolkit/go"
)

func TestStatsCLIEmptyHomeIsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, args := range [][]string{{"stats", "--json"}, {"stats"}, {"stats", "--top", "0"}, {"stats", "--repo", home}} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, nil, &out, &stderr)
		want := 0
		if len(args) > 2 {
			want = 2
		}
		if code != want {
			t.Fatalf("%v: %d %s", args, code, &stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".repokit")); !os.IsNotExist(err) {
			t.Fatalf("stats wrote state: %v", err)
		}
	}
}

// +case:id=stats_validation_reuse,desc=`Two checkouts each execute and then reuse lint`,expect=`Global statistics share the existing history projection without counting reused execution time again`
func TestStatsCLIAggregatesValidationAndPreservesLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".repokit")
	for range 2 {
		repo := t.TempDir()
		if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, output)
		}
		put(t, repo, "Makefile", "lint:\n\t@echo checked\n")
		put(t, repo, "source.py", "print('fixture')\n")
		req := repokit.ValidationRequest{Repository: repo, HistoryDir: filepath.Join(state, "repositories"), Checks: []string{"lint"}}
		for range 2 {
			report, err := repokit.Validate(context.Background(), req)
			if err != nil || !report.Passed {
				t.Fatalf("%+v %v", report, err)
			}
		}
	}
	put(t, state, "logs/2000-01-01.log", "damaged log\n")
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"stats", "--json"}, nil, &out, &stderr); code != 1 {
		t.Fatalf("%d %s", code, &stderr)
	}
	var report stats.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("%v: %s", err, &out)
	}
	if len(report.Validation.Repositories) != 2 || report.Validation.Total.Executions != 2 || report.Validation.Total.Reuses != 2 || report.Validation.ReuseRate != .5 || report.Validation.PassRate != 1 {
		t.Fatalf("%+v", report.Validation)
	}
	if report.Complete || len(report.Diagnostics) == 0 {
		t.Fatalf("%+v", report)
	}
	logs, err := os.ReadDir(filepath.Join(state, "logs"))
	if err != nil || len(logs) != 1 || logs[0].Name() != "2000-01-01.log" {
		t.Fatalf("stats logged or cleaned: %v %v", logs, err)
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"stats", "--home", state}, nil, &out, &stderr); code != 1 || !strings.Contains(out.String(), "2 executions") || !strings.Contains(out.String(), "Diagnostics") {
		t.Fatalf("%d %s %s", code, &out, &stderr)
	}
}
