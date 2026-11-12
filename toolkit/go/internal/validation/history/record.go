package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Counts counts real attempts separately from reuse. Seconds never includes
// the original run duration a second time when its result is reused.
type Counts struct {
	Executions       int       `json:"executions"`
	Passed           int       `json:"passed"`
	Failed           int       `json:"failed"`
	Unfinished       int       `json:"unfinished"`
	Other            int       `json:"other"`
	Reuses           int       `json:"reuses"`
	ExecutionSeconds float64   `json:"executionSeconds"`
	LookupSeconds    float64   `json:"lookupSeconds"`
	LastCheckedAt    time.Time `json:"lastCheckedAt,omitempty"`
}

// Summary describes retained records, not lifetime totals.
type Summary struct {
	Repository string            `json:"repository"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Checks     map[string]Counts `json:"checks"`
}

// Event records a Run transition and preserves the caller-owned check payload.
type Event struct {
	SchemaVersion int             `json:"schemaVersion"`
	Type          string          `json:"type"`
	Checkout      string          `json:"checkout"`
	At            time.Time       `json:"at"`
	Run           string          `json:"run"`
	Component     string          `json:"component"`
	Inputs        string          `json:"inputs"`
	Check         json.RawMessage `json:"check"`
	Consistent    bool            `json:"inputsConsistent,omitempty"`
}

// Run projects a start and optional completion; Result retains the full check payload.
type Run struct {
	FinishedAt   time.Time
	ReuseCount   int
	LastReusedAt time.Time
	Start        Event
	Result       json.RawMessage
	Check        string
	Status       string
	CheckedAt    time.Time
	Consistent   bool
}

// State is a replayed snapshot of retained Runs and repository statistics.
type State struct {
	Runs    map[string]*Run
	Order   []string // Completed Runs in WAL completion order; reuse does not reorder them.
	Summary Summary
	Inputs  map[string]bool
}

func newState(repository string) *State {
	return &State{Runs: map[string]*Run{}, Inputs: map[string]bool{}, Summary: Summary{Repository: repository, Checks: map[string]Counts{}}}
}

// +spec=Replay and live writes use the same projection; unfinished runs never grant evidence
// +spec=Orphan completions and reuses after rolling truncation grant neither evidence nor statistics
func (s *State) apply(e Event) error {
	var check outcome
	if err := json.Unmarshal(e.Check, &check); err != nil {
		return fmt.Errorf("invalid history check: %w", err)
	}
	if e.SchemaVersion != 2 || e.Checkout == "" || e.Run == "" || e.At.IsZero() || (check.Check != "lint" && check.Check != "test") {
		return errors.New("invalid validation history event")
	}
	key := e.Component + " " + check.Check
	counts := s.Summary.Checks[key]
	switch e.Type {
	case "started":
		if s.Runs[e.Run] != nil {
			return fmt.Errorf("duplicate run %s", e.Run)
		}
		s.Runs[e.Run] = &Run{Start: e, Check: check.Check}
		counts.Executions++
		counts.Unfinished++
	case "finished":
		run := s.Runs[e.Run]
		// Rolling truncation may leave a completion whose start has expired.
		if run == nil {
			return nil
		}
		if run.Result != nil || run.Start.Checkout != e.Checkout || run.Start.Component != e.Component || run.Check != check.Check || run.Start.Inputs != e.Inputs {
			return fmt.Errorf("invalid completion for run %s", e.Run)
		}
		s.Order = append(s.Order, e.Run)
		result := check
		run.Result, run.Status, run.CheckedAt, run.Consistent = e.Check, result.Status, result.CheckedAt, e.Consistent
		run.FinishedAt = e.At
		counts.Unfinished--
		counts.ExecutionSeconds += result.DurationSeconds
		counts.LookupSeconds += result.LookupSeconds
		if result.CheckedAt.After(counts.LastCheckedAt) {
			counts.LastCheckedAt = result.CheckedAt
		}
		switch result.Status {
		case "passed":
			counts.Passed++
		case "failed":
			counts.Failed++
		default:
			counts.Other++
		}
	case "reused":
		run := s.Runs[e.Run]
		if run == nil {
			return nil
		}
		if run.Result == nil || !run.Consistent || run.Status != "passed" || run.Start.Checkout != e.Checkout || run.Start.Component != e.Component || run.Check != check.Check {
			return fmt.Errorf("invalid reuse of run %s", e.Run)
		}
		run.ReuseCount++
		if e.At.After(run.LastReusedAt) {
			run.LastReusedAt = e.At
		}
		counts.Reuses++
		counts.LookupSeconds += check.LookupSeconds
	default:
		return fmt.Errorf("unknown history event %q", e.Type)
	}
	s.Inputs[e.Inputs] = true
	s.Summary.Checks[key] = counts
	if s.Summary.From.IsZero() || e.At.Before(s.Summary.From) {
		s.Summary.From = e.At
	}
	if e.At.After(s.Summary.To) {
		s.Summary.To = e.At
	}
	return nil
}

// outcome is the execution metadata needed for projection. The full check payload
// remains unchanged for its owner to interpret; history does not decide reuse.
type outcome struct {
	Check           string    `json:"check"`
	Status          string    `json:"status"`
	CheckedAt       time.Time `json:"checkedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	LookupSeconds   float64   `json:"lookupSeconds"`
}
