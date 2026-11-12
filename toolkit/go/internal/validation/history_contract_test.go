package validation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	repokit "github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/errs"
)

// +case=Go and TypeScript query the same retained WAL corpus, including corruption and nanosecond ordering.
func TestSharedHistoryQueries(t *testing.T) {
	var fixture struct {
		Events []json.RawMessage
		Cases  []struct {
			Name           string
			Query          repokit.ValidationRunQuery
			IDs            []string
			Total          int
			HasMore, Error bool
			ErrorCode      string
			Extra          []json.RawMessage
		}
	}
	data, err := os.ReadFile("../../../../conformance/history/runs.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Cases {
		t.Run(row.Name, func(t *testing.T) {
			root := validationRepo(t, "test:\n\t@false\n")
			dir := t.TempDir()
			empty, err := repokit.ListValidationRuns(context.Background(), repokit.ValidationRunQuery{HistoryDir: dir, Repository: root})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, empty.Repository, "history")
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			var contents []byte
			for i, event := range append(append([]json.RawMessage{}, fixture.Events...), row.Extra...) {
				line, _ := json.Marshal(map[string]string{"index": fmt.Sprint(i + 1), "data": "+" + string(event)})
				contents = append(contents, append(line, '\n')...)
			}
			if err := os.WriteFile(filepath.Join(path, "00000000000000000001"), contents, 0600); err != nil {
				t.Fatal(err)
			}
			q := row.Query
			q.Repository, q.HistoryDir = root, dir
			got, err := repokit.ListValidationRuns(context.Background(), q)
			if row.Error {
				if err == nil {
					t.Fatal("accepted invalid history/query")
				}
				var problem *errs.Error
				if !errors.As(err, &problem) || problem.Code != row.ErrorCode || problem.Op != "validation.history.list" || problem.Subject != root {
					t.Fatalf("incorrect classification: %+v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, run := range got.Runs {
				ids = append(ids, run.RunID)
				if run.RunID == "AAA" && run.ReuseCount != 1 {
					t.Fatal(run)
				}
			}
			if !reflect.DeepEqual(ids, row.IDs) || got.Total != row.Total || got.HasMore != row.HasMore {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// +case=Both retained-run query APIs read the same REPOKIT_HOME as the writer; a missing home stays absent.
func TestHistoryQueriesDefaultHome(t *testing.T) {
	ctx := context.Background()
	root := validationRepo(t, "test:\n\t@echo verified\n")
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	report, err := repokit.Validate(ctx, repokit.ValidationRequest{Repository: root, HistoryDir: filepath.Join(home, "repositories"), Checks: []string{"test"}})
	if err != nil || !report.Passed {
		t.Fatal(report, err)
	}
	list, err := repokit.ListValidationRuns(ctx, repokit.ValidationRunQuery{Repository: root})
	if err != nil || list.Total != 1 {
		t.Fatal(list, err)
	}
	detail, err := repokit.ReadValidationRun(ctx, "", root, list.Runs[0].RunID)
	if err != nil || !detail.Complete || detail.Result.Stdout != "verified\n" {
		t.Fatal(detail, err)
	}
	absent := filepath.Join(t.TempDir(), "absent")
	t.Setenv("REPOKIT_HOME", absent)
	list, err = repokit.ListValidationRuns(ctx, repokit.ValidationRunQuery{Repository: root})
	if err != nil || list.Total != 0 {
		t.Fatal(list, err)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("query created home: %v", err)
	}
}

// +case=The shared corpus fixes failure classification and separates input evidence from complete run evidence.
func TestSharedHistoryContracts(t *testing.T) {
	var corpus struct {
		Errors []struct{ Name, Operation, Mode, RunID, Code string }
		Inputs []struct {
			Name     string
			Inputs   json.RawMessage
			Complete bool
		}
	}
	data, err := os.ReadFile("../../../../conformance/history/contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, row := range corpus.Errors {
		t.Run(row.Name, func(t *testing.T) {
			root, dir, store := historyContractFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if row.Mode == "cancelled" {
				cancel()
			}
			if row.Mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
			}
			if row.Mode == "corrupt" {
				if err := os.WriteFile(filepath.Join(store, "history", "00000000000000000001"), []byte("broken\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			subject := root
			if row.Operation == "list" {
				_, err = repokit.ListValidationRuns(ctx, repokit.ValidationRunQuery{Repository: root, HistoryDir: dir})
			} else {
				subject = row.RunID
				_, err = repokit.ReadValidationRun(ctx, dir, root, row.RunID)
			}
			var problem *errs.Error
			if !errors.As(err, &problem) || problem.Code != row.Code || problem.Op != "validation.history."+row.Operation || problem.Subject != subject {
				t.Fatalf("incorrect classification: %+v", err)
			}
			if row.Mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation cause", err)
			}
			if row.Mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("lost deadline cause", err)
			}
		})
	}
	for _, row := range corpus.Inputs {
		t.Run(row.Name, func(t *testing.T) {
			root, dir, store := historyContractFixture(t)
			if row.Inputs != nil {
				if err := os.MkdirAll(filepath.Join(store, "inputs"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store, "inputs", "INPUT.json"), row.Inputs, 0600); err != nil {
					t.Fatal(err)
				}
			}
			q := repokit.ValidationRunQuery{Repository: root, HistoryDir: dir, IncludeInputs: true, Limit: 1, Offset: 2}
			got, err := repokit.ListValidationRuns(context.Background(), q)
			if err != nil || len(got.Runs) != 1 || got.Runs[0].RunID != "AAA" {
				t.Fatal(got, err)
			}
			summary := got.Runs[0].Inputs
			if summary == nil || summary.Complete != row.Complete {
				t.Fatal(summary)
			}
			if row.Complete {
				if summary.Snapshot != "digest" || len(summary.Diagnostics) != 0 {
					t.Fatal(summary)
				}
			} else if summary.Snapshot != "" || len(summary.Diagnostics) != 1 || summary.Diagnostics[0].Artifact != "inputs" {
				t.Fatal(summary)
			}
			detail, err := repokit.ReadValidationRun(context.Background(), dir, root, "AAA")
			if err != nil || detail.Complete || (detail.Inputs != nil) != row.Complete {
				t.Fatal(detail, err)
			}
			artifacts := []string{}
			for _, issue := range detail.Diagnostics {
				artifacts = append(artifacts, issue.Artifact)
			}
			want := []string{"output"}
			if !row.Complete {
				want = []string{"inputs", "output"}
			}
			if !reflect.DeepEqual(artifacts, want) {
				t.Fatal(artifacts)
			}
			q.IncludeInputs = false
			got, err = repokit.ListValidationRuns(context.Background(), q)
			if err != nil || got.Runs[0].Inputs != nil {
				t.Fatal(got, err)
			}
		})
	}
}

func historyContractFixture(t *testing.T) (root, dir, store string) {
	t.Helper()
	root = validationRepo(t, "test:\n\t@false\n")
	dir = t.TempDir()
	empty, err := repokit.ListValidationRuns(context.Background(), repokit.ValidationRunQuery{Repository: root, HistoryDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	store = filepath.Join(dir, empty.Repository)
	var corpus struct{ Events []json.RawMessage }
	data, err := os.ReadFile("../../../../conformance/history/runs.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store, "history")
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	var contents []byte
	for i, event := range corpus.Events {
		line, _ := json.Marshal(map[string]string{"index": fmt.Sprint(i + 1), "data": "+" + string(event)})
		contents = append(contents, append(line, '\n')...)
	}
	if err = os.WriteFile(filepath.Join(path, "00000000000000000001"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	return
}
