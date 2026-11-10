package history

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// +case:id=daily_output_roundtrip,desc=`Several runs share a date and finish after midnight`,expect=`UTC start date selects two files and each run restores its exact multiline output after reopening`
func TestDailyOutputRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	at := time.Date(2026, 11, 10, 1, 0, 0, 0, time.FixedZone("east", 8*3600))
	ids := []string{rand.Text(), rand.Text()}
	output := strings.Repeat("multi\nline\t\"output\"\n", 10000)
	for _, id := range ids {
		if err := h.SaveOutput(ctx, id, at, id+output, ""); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(h.dir, "logs"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v: %v", entries, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "2026-11-09.") {
			t.Fatal(entry.Name())
		}
	}
	reopened := newStore(filepath.Dir(h.dir), "fixture", "/fixture")
	for _, id := range ids {
		out, stderr, err := reopened.LoadOutput(ctx, id, at.UTC())
		if err != nil || out != id+output || stderr != "" {
			t.Fatalf("output mismatch: %v", err)
		}
	}
}

// +case:id=daily_output_legacy_partial,desc=`Legacy records coexist with daily records and a daily write is incomplete`,expect=`Legacy pairs remain readable but partial daily streams never authorize reuse`
func TestDailyOutputLegacyAndPartial(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	at := time.Now().UTC()
	id := rand.Text()
	for _, stream := range []string{"stdout", "stderr"} {
		if err := atomicFile(filepath.Join(h.dir, "logs", id, stream), []byte("legacy "+stream)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.SaveOutput(ctx, rand.Text(), at, "other", ""); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := h.LoadOutput(ctx, id, at)
	if err != nil || out != "legacy stdout" || stderr != "legacy stderr" {
		t.Fatalf("legacy: %q %q %v", out, stderr, err)
	}
	path := filepath.Join(h.dir, "logs", at.Format(time.DateOnly)+".stdout.jsonl")
	if err := appendOutput(path, outputRecord{id, "partial"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.LoadOutput(ctx, id, at); err == nil {
		t.Fatal("partial output accepted")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"runID":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SaveOutput(ctx, rand.Text(), at, "next", ""); err == nil {
		t.Fatal("appended after incomplete tail")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("corrupt tail changed")
	}
}

// +case:id=daily_output_retention,desc=`A daily file contains a retained run alongside orphan output`,expect=`Retained dates and fresh orphan files survive; expired unreferenced daily and legacy outputs are removed`
func TestDailyOutputRetention(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	at := time.Now().UTC().AddDate(0, 0, -60)
	events := testEvents(at)
	if err := h.Append(ctx, events); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveOutput(ctx, events[0].Run, at, "retained", ""); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		p := filepath.Join(h.dir, "logs", at.Format(time.DateOnly)+"."+stream+".jsonl")
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Maintain(ctx, 90); err != nil {
		t.Fatal(err)
	}
	if out, _, err := h.LoadOutput(ctx, events[0].Run, at); err != nil || out != "retained" {
		t.Fatalf("retained: %v", err)
	}
	// A late orphan written into an old date must survive the next sweep.
	freshAt := at.AddDate(0, 0, -1)
	freshID := rand.Text()
	if err := h.SaveOutput(ctx, freshID, freshAt, "fresh", ""); err != nil {
		t.Fatal(err)
	}
	legacyID := rand.Text()
	legacy := filepath.Join(h.dir, "logs", legacyID)
	if err := atomicFile(filepath.Join(legacy, "stdout"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacy, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Maintain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.LoadOutput(ctx, events[0].Run, at); !os.IsNotExist(err) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy expiry: %v", err)
	}
	if out, _, err := h.LoadOutput(ctx, freshID, freshAt); err != nil || out != "fresh" {
		t.Fatalf("fresh: %v", err)
	}
}
