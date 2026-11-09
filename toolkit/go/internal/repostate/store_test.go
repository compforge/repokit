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
	ctx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	err := WithLock(ctx, path, func(owned func() error) error {
		if err := Write(path, map[string]string{"value": "original"}, owned); err != nil {
			return err
		}
		waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if err := WithLock(waiting, path, func(func() error) error { t.Error("concurrent lease acquired"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
		cancelOwner()
		return Write(path, map[string]string{"value": "replacement"}, owned)
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

func TestReplacedLeaseCannotPublishOrRemoveNewOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	err := WithLock(context.Background(), path, func(owned func() error) error {
		if err := Write(path, map[string]int{"value": 1}, owned); err != nil {
			return err
		}
		if err := os.Rename(path+".lock", path+".old-lock"); err != nil {
			return err
		}
		if err := os.Mkdir(path+".lock", 0700); err != nil {
			return err
		}
		return Write(path, map[string]int{"value": 2}, owned)
	})
	if err == nil {
		t.Fatal("lost lease published")
	}
	var record map[string]int
	if _, err := Read(path, &record); err != nil || record["value"] != 1 {
		t.Fatal(record, err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("new owner's lock removed", err)
	}
}
