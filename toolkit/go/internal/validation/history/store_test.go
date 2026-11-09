package history

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/wal"
)

func testEvents(at time.Time) []Event {
	id, inputs := rand.Text(), rand.Text()
	result, _ := json.Marshal(outcome{Check: "lint", Status: "passed", CheckedAt: at, DurationSeconds: 2})
	start := Event{SchemaVersion: 2, Type: "started", Checkout: "/fixture", At: at, Run: id, Component: ".", Inputs: inputs, Check: result}
	end := start
	end.Type, end.Consistent = "finished", true
	return []Event{start, end}
}

// +case:id=history_wal_roll_reopen,desc=`History spans multiple WAL segments`,expect=`Reopening reconstructs identical run counts and durations`
func TestHistoryRollingAndReplay(t *testing.T) {
	h := newStore(t.TempDir(), "fixture", "/fixture")
	h.segmentSize = 128
	events := testEvents(time.Now().UTC())
	if err := h.Append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	reuse := events[1]
	reuse.Type = "reused"
	reuse.Check = json.RawMessage(`{"check":"lint","lookupSeconds":0.1}`)
	if err := h.Append(context.Background(), []Event{reuse}); err != nil {
		t.Fatal(err)
	}
	state, err := h.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counts := state.Summary.Checks[". lint"]
	if counts.Executions != 1 || counts.Passed != 1 || counts.Reuses != 1 || counts.ExecutionSeconds != 2 || counts.LookupSeconds != .1 || counts.Unfinished != 0 {
		t.Fatalf("%+v", counts)
	}
	files, err := os.ReadDir(filepath.Join(h.dir, "history"))
	if err != nil || len(files) < 2 {
		t.Fatalf("segments=%d: %v", len(files), err)
	}
}

func TestHistoryInterruptedAndCorruptTail(t *testing.T) {
	h := newStore(t.TempDir(), "fixture", "/fixture")
	events := testEvents(time.Now().UTC())
	if err := h.Append(context.Background(), events[:1]); err != nil {
		t.Fatal(err)
	}
	state, err := h.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c := state.Summary.Checks[". lint"]; c.Unfinished != 1 || c.Passed != 0 {
		t.Fatalf("%+v", c)
	}
	files, err := os.ReadDir(filepath.Join(h.dir, "history"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, "history", files[len(files)-1].Name())
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"index":`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := h.Read(context.Background()); !errors.Is(err, wal.ErrCorrupt) {
		t.Fatalf("wanted corruption: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("history repaired without authorization")
	}
}

// +case:id=history_expired_reuse_truncated,desc=`An expired success is referenced by a recent reuse`,expect=`Old runs are removed and orphan reuse grants no evidence or statistics`
func TestHistoryRetentionTruncatesReferences(t *testing.T) {
	h := newStore(t.TempDir(), "fixture", "/fixture")
	old := testEvents(time.Now().AddDate(0, 0, -60))
	retained := testEvents(time.Now().AddDate(0, 0, -45))
	recent := testEvents(time.Now())
	reuse := retained[1]
	reuse.Type = "reused"
	reuse.At = time.Now()
	events := append(append(append([]Event{}, old...), retained...), recent...)
	events = append(events, reuse)
	if err := h.Append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	state, err := h.Maintain(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[recent[0].Run] == nil {
		t.Fatal("recent reuse pinned an expired run")
	}
	if state.Inputs[retained[0].Inputs] {
		t.Fatal("orphan reuse retained expired input")
	}
	if c := state.Summary.Checks[". lint"]; c.Executions != 1 || c.Reuses != 0 {
		t.Fatalf("%+v", c)
	}
	// Expiring everything must produce an empty log that can accept new runs.
	if _, err := h.Maintain(context.Background(), -1); err != nil {
		t.Fatal(err)
	}
	if err := h.Append(context.Background(), testEvents(time.Now())); err != nil {
		t.Fatal(err)
	}
	state, err = h.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Summary.Checks[". lint"].Executions != 1 {
		t.Fatal(state.Summary)
	}
}

// +case:id=history_multiprocess_append,desc=`Independent CLI processes append to one checkout`,expect=`OS lock preserves all starts and completions across WAL reopen`
func TestHistoryMultipleProcesses(t *testing.T) {
	root := t.TempDir()
	processes := []*exec.Cmd{}
	for range 4 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHistoryWriterProcess$")
		cmd.Env = append(os.Environ(), "REPOKIT_HISTORY_TEST="+root)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		processes = append(processes, cmd)
	}
	for _, cmd := range processes {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	state, err := newStore(root, "fixture", "/fixture").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts := state.Summary.Checks[". lint"]; counts.Executions != 20 || counts.Passed != 20 || counts.Unfinished != 0 {
		t.Fatalf("%+v", counts)
	}
}

func TestHistoryWriterProcess(t *testing.T) {
	root := os.Getenv("REPOKIT_HISTORY_TEST")
	if root == "" {
		t.Skip("subprocess fixture")
	}
	h := newStore(root, "fixture", "/fixture")
	h.segmentSize = 512
	for range 5 {
		if err := h.Append(context.Background(), testEvents(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryLockCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := lock(ctx, path); err == nil {
		release()
		t.Fatal("second writer acquired lock")
	} else if !strings.Contains(err.Error(), "deadline") {
		t.Fatal(err)
	}
}

// +case:id=history_retention_during_reuse,desc=`Another session expires a selected Run before reuse is persisted`,expect=`No dangling reference is written and unrelated completions are retained`
func TestHistoryRetentionDuringReuse(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	old := testEvents(time.Now().AddDate(0, 0, -40))
	if err := h.Append(ctx, old); err != nil {
		t.Fatal(err)
	}
	selected, err := h.Maintain(ctx, 60)
	if err != nil || selected.Runs[old[0].Run] == nil {
		t.Fatalf("selection: %v", err)
	}
	current := testEvents(time.Now())
	if err := h.Append(ctx, current[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Maintain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	reuse := old[1]
	reuse.Type, reuse.At = "reused", time.Now()
	if err := h.Append(ctx, []Event{reuse, current[1]}); err != nil {
		t.Fatal(err)
	}
	// A batch consisting entirely of expired reuses must also be harmless.
	if err := h.Append(ctx, []Event{reuse}); err != nil {
		t.Fatal(err)
	}
	state, err := h.Maintain(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	counts := state.Summary.Checks[". lint"]
	if counts.Executions != 1 || counts.Passed != 1 || counts.Unfinished != 0 || counts.Reuses != 0 {
		t.Fatalf("%+v", counts)
	}
	if state.Runs[old[0].Run] != nil {
		t.Fatal("expired run resurrected")
	}
}

// +case:id=history_invalid_batch_atomic,desc=`A batch contains an invalid completion after a valid start`,expect=`The entire batch is rejected without corrupting persisted history`
func TestHistoryRejectsInvalidBatch(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	original := testEvents(time.Now())
	if err := h.Append(ctx, original); err != nil {
		t.Fatal(err)
	}
	next := testEvents(time.Now())
	invalid := next[1]
	invalid.Inputs = rand.Text()
	if err := h.Append(ctx, []Event{next[0], invalid}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	state, err := h.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[next[0].Run] != nil {
		t.Fatal("partial batch persisted")
	}
	if err := h.Append(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryRepositoryNames(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, output)
		}
	}
	git("init", "-q")
	base := t.TempDir()
	paths := map[string]bool{}
	for _, origin := range []string{
		"https://github.com/one/repo.git", "https://github.com/two/repo.git",
		"https://gitlab.com/one/repo.git", "https://github.com/one--two/repo.git",
		"https://github.com/one/two/repo.git", "https://github.com/one-/repo.git", "https://github.com/one/-repo.git",
	} {
		git("config", "remote.origin.url", origin)
		h, err := Open(ctx, base, root)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(h.dir) != base || paths[h.dir] {
			t.Fatalf("unsafe or colliding directory: %s", h.dir)
		}
		paths[h.dir] = true
	}
	git("config", "remote.origin.url", "https://user:secret@github.com/example/repo.git?token=hidden")
	first, err := Open(ctx, base, root)
	if err != nil {
		t.Fatal(err)
	}
	git("config", "remote.origin.url", "git@github.com:example/repo.git")
	second, err := Open(ctx, base, root)
	if err != nil || first.dir != second.dir || filepath.Base(first.dir) != "github--example--repo" {
		t.Fatalf("%+v %+v %v", first, second, err)
	}
}

func TestHistoryRejectsCrossCheckoutEvents(t *testing.T) {
	h := newStore(t.TempDir(), "fixture", "/fixture")
	ctx := context.Background()
	events := testEvents(time.Now())
	if err := h.Append(ctx, events); err != nil {
		t.Fatal(err)
	}
	reuse := events[1]
	reuse.Type, reuse.Checkout = "reused", "/other"
	if err := h.Append(ctx, []Event{reuse}); err == nil {
		t.Fatal("cross-checkout reference accepted")
	}
	state, err := h.Read(ctx)
	if err != nil || state.Summary.Checks[". lint"].Reuses != 0 {
		t.Fatalf("%+v %v", state, err)
	}
}

// +case:id=history_payload_roundtrip,desc=`Check and input records contain validation-owned fields`,expect=`Storage preserves the complete payload and output without interpreting reuse policy`
func TestPayloadRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	events := testEvents(time.Now())
	payload := json.RawMessage(`{"check":"lint","status":"passed","durationSeconds":2,"command":["make","lint"],"inputs":{"paths":["shared.cfg"]},"environment":"digest","extra":{"preserve":true}}`)
	events[0].Check, events[1].Check = payload, payload
	input := []byte(`{"schemaVersion":1,"checkout":"/fixture","files":{"a.go":{"kind":"file","digest":"abc"}}}`)
	if err := h.SaveInputs(events[0].Inputs, input); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveOutput(events[0].Run, "checked\n", "notice\n"); err != nil {
		t.Fatal(err)
	}
	if err := h.Append(ctx, events); err != nil {
		t.Fatal(err)
	}
	state, err := h.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(state.Runs[events[0].Run].Result) != string(payload) {
		t.Fatalf("payload lost: %s", state.Runs[events[0].Run].Result)
	}
	loaded, err := h.LoadInputs(events[0].Inputs)
	if err != nil || string(loaded) != string(input) {
		t.Fatalf("inputs: %s %v", loaded, err)
	}
	out, stderr, err := h.LoadOutput(events[0].Run)
	if err != nil || out != "checked\n" || stderr != "notice\n" {
		t.Fatalf("output: %q %q %v", out, stderr, err)
	}
}

// +case:id=history_rolling_expiry,desc=`Expired runs include successes, failures and unfinished checks`,expect=`All expire, late completions are ignored, retained runs and outputs remain usable`
func TestHistoryRetentionIgnoresRunStatus(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"passed", "failed", "unfinished"} {
		t.Run(status, func(t *testing.T) {
			h := newStore(t.TempDir(), "fixture", "/fixture")
			h.segmentSize = 128
			old := testEvents(time.Now().AddDate(0, 0, -60))
			if status == "failed" {
				old[1].Check = json.RawMessage(`{"check":"lint","status":"failed"}`)
			}
			events := old
			if status == "unfinished" {
				events = old[:1]
			}
			recent := testEvents(time.Now())
			if err := h.Append(ctx, append(events, recent...)); err != nil {
				t.Fatal(err)
			}
			for _, e := range []Event{old[0], recent[0]} {
				if err := h.SaveInputs(e.Inputs, []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
				if err := h.SaveOutput(e.Run, "output", ""); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{filepath.Join(h.dir, "inputs", old[0].Inputs+".json"), filepath.Join(h.dir, "logs", old[0].Run)} {
				if err := os.Chtimes(path, old[0].At, old[0].At); err != nil {
					t.Fatal(err)
				}
			}
			state, err := h.Maintain(ctx, 30)
			if err != nil || len(state.Runs) != 1 || state.Runs[recent[0].Run] == nil {
				t.Fatalf("state=%+v: %v", state, err)
			}
			if _, err := h.LoadInputs(old[0].Inputs); !os.IsNotExist(err) {
				t.Fatalf("expired inputs: %v", err)
			}
			if _, _, err := h.LoadOutput(old[0].Run); !os.IsNotExist(err) {
				t.Fatalf("expired output: %v", err)
			}
			if output, _, err := h.LoadOutput(recent[0].Run); err != nil || output != "output" {
				t.Fatalf("retained output: %q %v", output, err)
			}
			if status == "unfinished" {
				late := old[1]
				late.At = time.Now()
				next := testEvents(time.Now())
				if err := h.Append(ctx, append([]Event{late}, next...)); err != nil {
					t.Fatal(err)
				}
				state, err = h.Read(ctx)
				if err != nil || state.Runs[old[0].Run] != nil || len(state.Runs) != 2 {
					t.Fatalf("late completion resurrected run: %+v %v", state, err)
				}
			}
		})
	}
}

func TestHistoryRetentionAcrossCompletion(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	old := testEvents(time.Now().AddDate(0, 0, -40))
	old[1].At = time.Now()
	if err := h.Append(ctx, old); err != nil {
		t.Fatal(err)
	}
	state, err := h.Maintain(ctx, 30)
	if err != nil || len(state.Runs) != 0 || len(state.Summary.Checks) != 0 {
		t.Fatalf("orphan completion granted evidence: %+v %v", state, err)
	}
	// Reopening must tolerate the orphan that remains in the retained suffix.
	if _, err := h.Read(ctx); err != nil {
		t.Fatal(err)
	}
	recent := testEvents(time.Now())
	if err := h.Append(ctx, recent); err != nil {
		t.Fatal(err)
	}
	state, err = h.Read(ctx)
	if err != nil || state.Summary.Checks[". lint"].Executions != 1 {
		t.Fatalf("%+v %v", state, err)
	}
}

// +case:id=history_completion_order,desc=`Concurrent runs finish in a different order and an old success is reused later`,expect=`Candidate ordering follows completions and never promotes the reused success`
func TestHistoryCompletionOrder(t *testing.T) {
	ctx := context.Background()
	h := newStore(t.TempDir(), "fixture", "/fixture")
	failure, success := testEvents(time.Now()), testEvents(time.Now())
	failure[1].Check = json.RawMessage(`{"check":"lint","status":"failed"}`)
	reuse := success[1]
	reuse.Type = "reused"
	if err := h.Append(ctx, []Event{failure[0], success[0], success[1], failure[1], reuse}); err != nil {
		t.Fatal(err)
	}
	state, err := h.Read(ctx)
	if err != nil || !slices.Equal(state.Order, []string{success[0].Run, failure[0].Run}) {
		t.Fatalf("%+v %v", state, err)
	}
}
