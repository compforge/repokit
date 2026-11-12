package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// +case=Run queries leave expired records untouched, and reject corrupt history instead of returning a partial list.
func TestReadRetainedDoesNotPruneOrRepair(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	events := testEvents(time.Now().AddDate(0, 0, -90))
	if err := h.Append(ctx, events); err != nil {
		t.Fatal(err)
	}
	state, err := h.ReadRetained(ctx)
	if err != nil || len(state.Runs) != 1 {
		t.Fatal(state, err)
	}
	replayed, err := h.Read(ctx)
	if err != nil || !reflect.DeepEqual(state, replayed) {
		t.Fatal(replayed, err)
	}
	entries, _ := os.ReadDir(filepath.Join(h.dir, "history"))
	path := filepath.Join(h.dir, "history", entries[len(entries)-1].Name())
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("broken\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := h.ReadRetained(ctx); err == nil {
		t.Fatal("accepted corrupted history")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("read repaired history")
	}
}

func TestReadRetainedHonorsLockCancellation(t *testing.T) {
	h := newStore(t.TempDir(), "fixture", "/fixture")
	if err := h.Append(context.Background(), testEvents(time.Now())); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(context.Background(), filepath.Join(h.dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := h.ReadRetained(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
