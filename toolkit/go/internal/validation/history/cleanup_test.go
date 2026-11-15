package history

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// +case=Count cleanup preserves a fixed newest window, dry-run leaves WAL unchanged, and zero evicts all completed runs.
func TestCountCleanup(t *testing.T) {
	base := t.TempDir()
	h := newStore(base, "fixture", "/fixture")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := h.Append(ctx, testEvents(time.Now().Add(time.Duration(i-3)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := os.ReadDir(filepath.Join(h.dir, "history"))
	before := map[string]string{}
	for _, f := range files {
		p := filepath.Join(h.dir, "history", f.Name())
		data, _ := os.ReadFile(p)
		before[p] = string(data)
	}
	report, err := Clean(ctx, base, "fixture", 30, 1, true)
	if err != nil || report.Before != 3 || report.After != 1 {
		t.Fatal(report, err)
	}
	for p, data := range before {
		after, _ := os.ReadFile(p)
		if string(after) != data {
			t.Fatal("dry-run changed WAL")
		}
	}
	report, err = Clean(ctx, base, "fixture", 30, 1, false)
	if err != nil || report.After != 1 {
		t.Fatal(report, err)
	}
	report, err = Clean(ctx, base, "fixture", 30, 0, false)
	if err != nil || report.After != 0 {
		t.Fatal(report, err)
	}
	state, err := h.ReadRetained(ctx)
	if err != nil || len(state.Runs) != 0 {
		t.Fatal(state, err)
	}
}

// +case=An unfinished old run pins the WAL prefix without displacing newer completed runs.
func TestCountCleanupProtectsUnfinished(t *testing.T) {
	base := t.TempDir()
	h := newStore(base, "fixture", "/fixture")
	ctx := context.Background()
	old := testEvents(time.Now().Add(-time.Minute))
	h.Append(ctx, old[:1])
	h.Append(ctx, testEvents(time.Now()))
	report, err := Clean(ctx, base, "fixture", 30, 1, false)
	if err != nil || report.After != 2 || report.OverBudget != 1 {
		t.Fatal(report, err)
	}
}
