package validation_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	repokit "github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/validation/history"
)

// +case=Actual runs, reuses and failures remain distinct; querying history never runs Make or grants a current pass.
func TestValidationRunsExecutionReuseAndEvidence(t *testing.T) {
	ctx := context.Background()
	root := validationRepo(t, "lint:\n\t@echo executed >> runs.out\n\t@echo checked\ntest:\n\t@echo failed-check >&2\n\t@false\n")
	dir := t.TempDir()
	req := repokit.ValidationRequest{Repository: root, HistoryDir: dir, Checks: []string{"lint"}}
	first, err := repokit.Validate(ctx, req)
	if err != nil || !first.Passed {
		t.Fatal(first, err)
	}
	original := first.Components[0].Checks[0]
	second, err := repokit.Validate(ctx, req)
	if err != nil || !second.Components[0].Checks[0].Reused {
		t.Fatal(second, err)
	}
	req.Checks = []string{"test"}
	failed, err := repokit.Validate(ctx, req)
	if err != nil || failed.Passed {
		t.Fatal(failed, err)
	}
	q := repokit.ValidationRunQuery{HistoryDir: dir, Repository: root, Limit: 1}
	list, err := repokit.ListValidationRuns(ctx, q)
	if err != nil || list.Total != 2 || !list.HasMore || len(list.Runs) != 1 || list.Runs[0].Status != "failed" {
		t.Fatal(list, err)
	}
	q.Offset = 1
	next, err := repokit.ListValidationRuns(ctx, q)
	if err != nil || next.HasMore || len(next.Runs) != 1 || next.Runs[0].RunID != original.RunID || next.Runs[0].ReuseCount != 1 || next.Runs[0].LastReusedAt == nil {
		t.Fatal(next, err)
	}
	q.Offset, q.Check, q.Status, q.Component, q.Checkout = 0, "lint", "passed", ".", root
	filtered, err := repokit.ListValidationRuns(ctx, q)
	if err != nil || filtered.Total != 1 || filtered.Runs[0].RunID != original.RunID {
		t.Fatal(filtered, err)
	}
	detail, err := repokit.ReadValidationRun(ctx, dir, root, original.RunID)
	if err != nil || !detail.Complete || detail.Inputs == nil || detail.Inputs.Snapshot != first.Snapshot || !reflect.DeepEqual(detail.Result, original) || detail.Run.InputsConsistent == nil || !*detail.Run.InputsConsistent {
		t.Fatal(detail, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "runs.out"))
	if string(data) != "executed\n" {
		t.Fatal("query executed project code", string(data))
	}
	// Missing artifacts preserve the known successful Run, but cannot be complete evidence.
	entries, _ := os.ReadDir(dir)
	store := filepath.Join(dir, entries[0].Name())
	inputPath := filepath.Join(store, "inputs", detail.Run.InputID+".json")
	if err := os.WriteFile(inputPath, []byte("invalid-json"), 0600); err != nil {
		t.Fatal(err)
	}
	malformed, err := repokit.ReadValidationRun(ctx, dir, root, original.RunID)
	if err != nil || malformed.Complete || malformed.Inputs != nil || len(malformed.Diagnostics) != 1 || malformed.Diagnostics[0].Artifact != "inputs" || malformed.Result.Stdout != original.Stdout {
		t.Fatal(malformed, err)
	}
	if err := os.Remove(filepath.Join(store, "inputs", detail.Run.InputID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(store, "logs")); err != nil {
		t.Fatal(err)
	}
	partial, err := repokit.ReadValidationRun(ctx, dir, root, original.RunID)
	if err != nil || partial.Complete || len(partial.Diagnostics) != 2 || partial.Run.Status != "passed" || partial.Inputs != nil {
		t.Fatal(partial, err)
	}
}

// +case=Retained unfinished runs and deleted checkout paths remain queryable, without claiming that a process is still running.
func TestValidationRunsUnfinishedAndDeletedCheckout(t *testing.T) {
	ctx := context.Background()
	root := validationRepo(t, "lint:\n\t@true\n")
	dir := t.TempDir()
	store, err := history.Open(ctx, dir, root)
	if err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(root, "removed-checkout")
	id, inputID := rand.Text(), rand.Text()
	check := repokit.CheckResult{CheckSpec: repokit.CheckSpec{Check: "test", Scope: "focused", Files: []string{"test_example.py"}}, RunID: id}
	payload, _ := json.Marshal(check)
	e := history.Event{SchemaVersion: 2, Type: "started", Checkout: removed, At: time.Now().UTC(), Run: id, Component: "pkg", Inputs: inputID, Check: payload}
	if err := store.Append(ctx, []history.Event{e}); err != nil {
		t.Fatal(err)
	}
	q := repokit.ValidationRunQuery{HistoryDir: dir, Repository: root, Checkout: removed, Status: "unfinished"}
	list, err := repokit.ListValidationRuns(ctx, q)
	if err != nil || list.Total != 1 || list.Runs[0].InputsConsistent != nil || list.Runs[0].FinishedAt != nil {
		t.Fatal(list, err)
	}
	detail, err := repokit.ReadValidationRun(ctx, dir, root, id)
	if err != nil || detail.Complete || detail.Result.Status != "unfinished" || detail.Run.Scope != "focused" {
		t.Fatal(detail, err)
	}
}

func TestValidationRunsMissingInvalidAndCanceled(t *testing.T) {
	ctx := context.Background()
	root := validationRepo(t, "test:\n\t@true\n")
	dir := filepath.Join(t.TempDir(), "absent")
	q := repokit.ValidationRunQuery{HistoryDir: dir, Repository: root}
	list, err := repokit.ListValidationRuns(ctx, q)
	if err != nil || list.Runs == nil || list.Total != 0 {
		t.Fatal(list, err)
	}
	_, err = repokit.ReadValidationRun(ctx, dir, root, rand.Text())
	var problem *errs.Error
	if !errors.As(err, &problem) || problem.Code != "validation.run_not_found" {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("query created history", err)
	}
	for _, bad := range []repokit.ValidationRunQuery{
		{HistoryDir: dir, Repository: root, Limit: -1}, {HistoryDir: dir, Repository: root, Limit: 101},
		{HistoryDir: dir, Repository: root, Offset: -1}, {HistoryDir: dir, Repository: root, Check: "format"},
		{HistoryDir: dir, Repository: root, Status: "running"},
	} {
		if _, err := repokit.ListValidationRuns(ctx, bad); err == nil {
			t.Fatal("accepted invalid query", bad)
		}
	}
	if _, err := repokit.ReadValidationRun(ctx, dir, root, "../../private"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repokit.ListValidationRuns(canceled, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
