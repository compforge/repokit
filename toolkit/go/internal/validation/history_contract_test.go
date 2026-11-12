package validation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
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
			ErrorMessage   string
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
				if row.ErrorMessage != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(row.ErrorMessage)) {
					t.Fatalf("expected error containing %q, got %v", row.ErrorMessage, err)
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
