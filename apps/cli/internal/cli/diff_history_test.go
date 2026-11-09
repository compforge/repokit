package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
)

func historyPath(home string) string {
	return filepath.Join(home, ".repokit", "logs", "diff-"+time.Now().Format("2006-01-02")+".jsonl")
}

func readDiffHistory(t *testing.T, home string) []diffRecord {
	t.Helper()
	data, err := os.ReadFile(historyPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("unterminated record")
	}
	var records []diffRecord
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var record diffRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("corrupt history: %v", err)
		}
		records = append(records, record)
	}
	return records
}

func TestDiffHistoryCapturesComparisonAndAppends(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := fixture(t)
	base := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	put(t, repo, "source file.ts", "export function a() { return 9; }\nexport function b() { return 2; }\n")
	args := []string{"impact", "--repo", repo, "--test-dir", "tests", "--changed-file", "source file.ts", "--timeout", "15s", "--max-files", "42", "--max-nodes", "2000", "--max-relations", "5000", "--max-snapshot-bytes", "268435456"}
	report := runJSON(t, append(args, "--json"), "")
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("text diff: %d %s", code, stderr.String())
	}
	records := readDiffHistory(t, home)
	if len(records) != 2 {
		t.Fatalf("records: %d", len(records))
	}
	for _, record := range records {
		if record.MaxFiles != 42 || record.MaxNodes != 2000 || record.MaxRelations != 5000 || record.MaxSnapshotBytes != 268435456 {
			t.Fatalf("lost replay budgets: %+v", record)
		}
		if record.SchemaVersion != 5 || record.Status != "completed" || record.Version != Version || record.From != base || record.To != "working_tree" || record.Input != report.Input || record.Checkout != report.Checkout || record.Snapshot != report.Snapshot {
			t.Fatalf("identity: %+v", record)
		}
		if record.Timeout != "15s" || !reflect.DeepEqual(record.TestDirs, []string{"tests"}) || !reflect.DeepEqual(record.ChangedFiles, []string{"source file.ts"}) {
			t.Fatalf("query: %+v", record)
		}
		if record.Time.IsZero() || !strings.Contains(readLogs(t, home), "run_id="+record.RunID) {
			t.Fatal("missing execution link")
		}
		snapshot := record.Timeline
		if snapshot.ID != record.RunID || snapshot.Operation != "impact" || snapshot.Status != timeline.Succeeded {
			t.Fatalf("operation: %+v", snapshot)
		}
		if len(snapshot.RunningStages()) != 0 || len(snapshot.Stages) != 7 || snapshot.Stages[0].Name != "analysis.snapshot" {
			t.Fatalf("missing or unfinished stage timings: %+v", snapshot)
		}
		stages := map[string]timeline.Stage{}
		for _, stage := range snapshot.Stages {
			stages[stage.Name] = stage
			if stage.Status != timeline.Succeeded || stage.StartedAt.Before(snapshot.StartedAt) || stage.FinishedAt.After(snapshot.FinishedAt) || stage.Duration(snapshot.CapturedAt) < 0 {
				t.Fatalf("invalid stage timing: %+v", stage)
			}
		}
		for _, name := range []string{"analysis.snapshot", "analysis.impact", "analysis.finalize"} {
			if stages[name].ParentID != "" {
				t.Fatalf("top-level parent: %+v", stages[name])
			}
		}
		for _, name := range []string{"workset.before", "workset.after", "query.before", "query.after"} {
			stage, parent := stages[name], stages["analysis.impact"]
			if stage.ParentID != parent.ID || stage.StartedAt.Before(parent.StartedAt) || stage.FinishedAt.After(parent.FinishedAt) {
				t.Fatalf("nested interval: %+v under %+v", stage, parent)
			}
			key := "parsedFiles"
			if strings.HasPrefix(name, "query.") {
				key = "candidates"
			}
			if count, ok := timeline.AttributeValue[int](stage.Attributes, key); !ok || count < 1 {
				t.Fatalf("lost %s: %+v", key, stage)
			}
		}
	}
	if records[0].RunID == records[1].RunID {
		t.Fatal("reused run ID")
	}
	info, err := os.Stat(historyPath(home))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
}

func TestDiffHistoryCommitIndexPatchAndPartial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := fixture(t)
	base := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	put(t, repo, "source file.ts", "export function a() { return 9; }\nexport function b() { return 2; }\n")
	patch := gitCommand(t, repo, "diff", "--binary", "HEAD")
	gitCommand(t, repo, "add", "source file.ts")
	for _, flags := range [][]string{{"--staged"}, {"--file", "-"}} {
		report := runJSON(t, append([]string{"impact", "--repo", repo, "--json"}, flags...), patch)
		records := readDiffHistory(t, home)
		last := records[len(records)-1]
		if last.To != report.Input || last.Snapshot != report.Snapshot {
			t.Fatalf("mutable input: %+v", last)
		}
		if report.Input == "patch" && last.PatchFile != "-" {
			t.Fatal("lost stdin patch mode")
		}
	}
	gitCommand(t, repo, "commit", "-qm", "change")
	head := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	runJSON(t, []string{"impact", "--repo", repo, "--base", base, "--head", "HEAD", "--json"}, "")
	records := readDiffHistory(t, home)
	if records[2].From != base || records[2].To != head || records[2].Input != "commit" {
		t.Fatal(records[2])
	}
	put(t, repo, "resource.mystery", "changed resource\n")
	report := runJSON(t, []string{"impact", "--repo", repo, "--test-dir", "tests", "--json"}, "")
	records = readDiffHistory(t, home)
	last := records[len(records)-1]
	if report.Complete || len(report.Diagnostics) == 0 || last.Status != "completed" {
		t.Fatalf("partial analysis: %+v", last)
	}
	data, err := os.ReadFile(historyPath(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"scope", "complete", "diagnostics", "testFiles"} {
			if _, ok := raw[field]; ok {
				t.Fatalf("analysis result retained in history: %s", field)
			}
		}

	}
}

func TestDiffHistoryFailureDoesNotChangeResult(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(historyPath(home), 0700); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"impact", "--repo", fixture(t), "--json"}, nil, &out, &stderr)
	if code != 0 || !json.Valid(out.Bytes()) || strings.Count(stderr.String(), "repocli: log warning:") != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
}

func TestDiffHistoryRecordsAnalysisFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := fixture(t)
	for _, args := range [][]string{{"version"}, {"snapshot", "--repo", repo}, {"impact", "--help"}, {"impact", "--impact", "invalid"}} {
		var out, stderr bytes.Buffer
		Execute(context.Background(), args, nil, &out, &stderr)
	}
	if _, err := os.Stat(historyPath(home)); !os.IsNotExist(err) {
		t.Fatalf("unexpected history before analysis: %v", err)
	}
	var failedOut, failedErr bytes.Buffer
	if code := Execute(context.Background(), []string{"impact", "--repo", repo, "--base", "absent-ref"}, nil, &failedOut, &failedErr); code == 0 {
		t.Fatal("missing base unexpectedly succeeded")
	}
	failed := readDiffHistory(t, home)
	if len(failed) != 1 || failed[0].Status != "failed" || len(failed[0].Timeline.Stages) != 1 || failed[0].Timeline.Stages[0].Name != "analysis.snapshot" {
		t.Fatalf("failed analysis history: %+v", failed)
	}
	assertFailedTimeline(t, failed[0], timeline.Failed)
	var stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"impact", "--repo", repo, "--json"}, nil, failedWriter{}, &stderr); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	records := readDiffHistory(t, home)
	if len(records) != 2 || records[1].Status != "completed" {
		t.Fatal("completed analysis lost on stdout failure")
	}
}

func TestDiffAnalysisStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "completed"},
		{context.DeadlineExceeded, "deadline_exceeded"},
		{context.Canceled, "canceled"},
		{os.ErrNotExist, "failed"},
	} {
		if got := diffAnalysisStatus(tc.err); got != tc.want {
			t.Fatalf("status(%v) = %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestDiffHistoryCancellationIncludesTimeline(t *testing.T) {
	for _, status := range []string{"deadline_exceeded", "canceled"} {
		t.Run(status, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			repo := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			if status == "deadline_exceeded" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			var out, stderr bytes.Buffer
			if code := Execute(ctx, []string{"impact", "--repo", repo}, nil, &out, &stderr); code == 0 {
				t.Fatal("canceled context unexpectedly succeeded")
			}
			records := readDiffHistory(t, home)
			if len(records) != 1 || records[0].Status != status {
				t.Fatalf("cancellation history: %+v", records)
			}
			assertFailedTimeline(t, records[0], timeline.Canceled)
		})
	}
}

func assertFailedTimeline(t *testing.T, record diffRecord, status timeline.Status) {
	t.Helper()
	snapshot := record.Timeline
	stage, ok := snapshot.LatestFailedStage()
	if !ok || snapshot.Status != status || snapshot.Error == "" || stage.Status != status || stage.Error == "" || stage.Name != "analysis.snapshot" || stage.ParentID != "" || len(snapshot.Stages) != 1 || len(snapshot.RunningStages()) != 0 {
		t.Fatalf("failed snapshot: %+v", snapshot)
	}
}

func TestConcurrentDiffHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := fixture(t)
	const count = 6
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), []string{"impact", "--repo", repo, "--json"}, nil, &out, &stderr); code != 0 || stderr.Len() != 0 {
				t.Errorf("exit=%d stderr=%s", code, stderr.String())
			}
		})
	}
	wg.Wait()
	records := readDiffHistory(t, home)
	if len(records) != count {
		t.Fatalf("records=%d", len(records))
	}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.RunID] {
			t.Fatal("duplicate run")
		}
		seen[record.RunID] = true
	}
}

func TestDiffHistoryRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	expired := "diff-" + now.AddDate(0, 0, -30).Format("2006-01-02") + ".jsonl"
	kept := []string{"diff-" + now.AddDate(0, 0, -29).Format("2006-01-02") + ".jsonl", "diff-2026-03-15.jsonl", "other-2020-01-01.jsonl", "diff-invalid.jsonl", "diff-2020-01-01.jsonl.bak"}
	for _, name := range append(kept, expired) {
		put(t, dir, name, "record")
	}
	if err := os.Mkdir(filepath.Join(dir, "diff-2020-01-01.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, kept[0]), filepath.Join(dir, "diff-2020-01-02.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := pruneLogs(dir, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, expired)); !os.IsNotExist(err) {
		t.Fatalf("expired: %v", err)
	}
	for _, name := range append(kept, "diff-2020-01-01.jsonl", "diff-2020-01-02.jsonl") {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("removed %s: %v", name, err)
		}
	}
}
