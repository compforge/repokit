package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrRunNotFound means a successful replay did not contain the requested Run.
var ErrRunNotFound = errors.New("run is not retained")

// EvidenceIssue identifies an unavailable artifact without discarding the Run.
type EvidenceIssue struct {
	Artifact string `json:"artifact"`
	Message  string `json:"message"`
}

type RunEvidence struct {
	Repository string
	Run        *Run
	Inputs     []byte
	Stdout     string
	Stderr     string
	Issues     []EvidenceIssue
}

// Inspect reads retained facts without creating a store, recording access or pruning.
// The callback may load input evidence under the same lock; it must not retain the store view.
// +spec=Replay and evidence reads share the repository lock; corruption is never a partial successful history.
func (h *Store) Inspect(ctx context.Context, fn func(*State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(h.dir, "history"))
	if errors.Is(err, os.ErrNotExist) {
		return fn(newState(h.repository))
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a regular history directory")
	}
	unlock, err := lock(ctx, filepath.Join(h.dir, "lock"))
	if err != nil {
		return err
	}
	defer unlock()
	state, err := h.readRetained(ctx)
	if err != nil {
		return err
	}
	return fn(state)
}

func (h *Store) ReadRetained(ctx context.Context) (*State, error) {
	var state *State
	err := h.Inspect(ctx, func(s *State) error { state = s; return nil })
	return state, err
}

func (h *Store) ReadRun(ctx context.Context, id string) (RunEvidence, error) {
	evidence := RunEvidence{Repository: h.repository, Issues: []EvidenceIssue{}}
	if !validID(id) {
		return evidence, errors.New("invalid run ID")
	}
	err := h.Inspect(ctx, func(state *State) error {
		run := state.Runs[id]
		if run == nil {
			return ErrRunNotFound
		}
		evidence.Run = run
		var err error
		evidence.Inputs, err = h.LoadInputs(run.Start.Inputs)
		if err != nil {
			evidence.Issues = append(evidence.Issues, EvidenceIssue{"inputs", err.Error()})
		}
		evidence.Stdout, evidence.Stderr, err = h.loadOutput(ctx, id, run.Start.At)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			evidence.Issues = append(evidence.Issues, EvidenceIssue{"output", err.Error()})
		}
		return ctx.Err()
	})
	return evidence, err
}
