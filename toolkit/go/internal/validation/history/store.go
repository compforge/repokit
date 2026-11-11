package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/project"
	repostate "github.com/compforge/repokit/toolkit/go/state"
	"github.com/tidwall/wal"
)

// Store serializes repository history access; it never holds a lock while checks run.
type Store struct {
	dir         string
	checkout    string
	repository  string
	segmentSize int
	trackAccess bool
}

func newStore(base, repository, checkout string) *Store {
	return &Store{dir: filepath.Join(base, repository), repository: repository, checkout: checkout}
}

// Open identifies a repository using its credential-free origin. Local repositories
// use shared Git metadata so linked worktrees still share one store.
func Open(ctx context.Context, base, checkout string) (*Store, error) {
	repo, err := git.Open(ctx, checkout)
	if err != nil {
		return nil, err
	}
	origin, err := repo.Origin(ctx)
	if err != nil {
		return nil, err
	}
	var name string
	if identity := project.FromOrigin(origin); identity != nil {
		name = repostate.RepositoryKey(identity.Forge.Name, identity.Path)
	} else {
		common, err := repo.CommonDir(ctx)
		if err != nil {
			return nil, err
		}
		name = repostate.LocalKey(common)
	}
	store := newStore(base, name, checkout)
	store.trackAccess = filepath.Base(base) == "repositories"
	return store, nil
}

// WAL caches file offsets and sequence numbers. Hold the OS lock over its entire
// open/read/write/close lifetime, never across a Make invocation.
// +spec=Independent CLI processes serialize history access without serializing checks
func (h *Store) access(ctx context.Context, fn func(*wal.Log) error) (err error) {
	if h.trackAccess {
		if err = repostate.RecordAccess(ctx, h.checkout, filepath.Dir(filepath.Dir(h.dir))); err != nil {
			return err
		}
	}
	if err = os.MkdirAll(h.dir, 0700); err != nil {
		return err
	}
	unlock, err := lock(ctx, filepath.Join(h.dir, "lock"))
	if err != nil {
		return err
	}
	defer unlock()
	log, err := wal.Open(filepath.Join(h.dir, "history"), &wal.Options{LogFormat: wal.JSON, SegmentSize: h.segmentSize, AllowEmpty: true, DirPerms: 0700, FilePerms: 0600})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, log.Close()) }()
	return fn(log)
}

func (h *Store) replay(ctx context.Context, log *wal.Log) (*State, error) {
	state := newState(h.repository)
	first, err := log.FirstIndex()
	if err != nil {
		return nil, err
	}
	last, err := log.LastIndex()
	if err != nil {
		return nil, err
	}
	for index := first; index <= last; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := log.Read(index)
		if err != nil {
			return nil, err
		}
		var e Event
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("history entry %d: %w", index, err)
		}
		if err := state.apply(e); err != nil {
			return nil, fmt.Errorf("history entry %d: %w", index, err)
		}
	}
	return state, nil
}

func (h *Store) Read(ctx context.Context) (*State, error) {
	var state *State
	err := h.access(ctx, func(log *wal.Log) error {
		var err error
		state, err = h.replay(ctx, log)
		return err
	})
	return state, err
}

func (h *Store) Append(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	// +spec=Validate the complete batch against current state under the WAL write lock
	return h.access(ctx, func(log *wal.Log) error {
		state, err := h.replay(ctx, log)
		if err != nil {
			return err
		}
		index, err := log.LastIndex()
		if err != nil {
			return err
		}
		batch := new(wal.Batch)
		for _, e := range events {
			// Retention may remove a run while its execution or reuse is in flight.
			// Losing history never changes the check result or resurrects the run.
			if (e.Type == "finished" || e.Type == "reused") && state.Runs[e.Run] == nil {
				slog.WarnContext(ctx, "validation run history expired", "checkout", h.checkout, "run", e.Run, "event", e.Type)
				continue
			}
			if err := state.apply(e); err != nil {
				return fmt.Errorf("append validation history: %w", err)
			}
			data, err := json.Marshal(e)
			if err != nil {
				return err
			}
			index++
			batch.Write(index, data)
		}
		return log.WriteBatch(batch)
	})
}

// ReadSummary reads retained repository statistics without executing checks.
func ReadSummary(ctx context.Context, directory, checkout string) (Summary, error) {
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return Summary{}, err
	}
	checkout, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		return Summary{}, err
	}
	if directory == "" {
		return Summary{}, errors.New("history directory is required")
	}
	store, err := Open(ctx, directory, checkout)
	if err != nil {
		return Summary{}, err
	}
	store.trackAccess = false
	state, err := store.Read(ctx)
	if err != nil {
		return Summary{}, err
	}
	return state.Summary, nil
}
