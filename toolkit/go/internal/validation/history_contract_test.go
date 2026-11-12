package validation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
