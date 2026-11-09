package repostate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseCancellationAndAtomicRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	ctx := context.Background()
	err := WithLock(ctx, path, func(owned func() error) error {
		if err := Write(path, map[string]string{"value": "original"}, owned); err != nil {
			return err
		}
		waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if err := WithLock(waiting, path, func(func() error) error { t.Error("concurrent lease acquired"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
		return Write(path, map[string]string{"value": "replacement"}, func() error { return context.Canceled })
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var record map[string]string
	if _, err := Read(path, &record); err != nil || record["value"] != "original" {
		t.Fatal(record, err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("lock not released", err)
	}
	os.WriteFile(path, []byte("{"), 0600)
	if _, err := Read(path, &record); err == nil {
		t.Fatal("corruption hidden")
	}
}
