package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	repostate "github.com/compforge/repokit/toolkit/go/state"
	"github.com/tidwall/wal"
)

// Maintain truncates expired events regardless of Run status or references.
// +spec=Unfinished Runs and recent reuses never pin the expired history prefix
// +why=History is disposable evidence; losing a Run requires execution instead of unbounded retention
// +link=component://docs/validation.md
func (h *Store) Maintain(ctx context.Context, days int) (*State, error) {
	maximum := 1000
	if h.trackAccess {
		policy, err := repostate.LoadCleanupPolicy(filepath.Dir(filepath.Dir(filepath.Dir(h.dir))), h.repository)
		if err != nil {
			return nil, err
		}
		maximum = policy.Validation.MaxCount
	}
	return h.maintain(ctx, days, maximum, false)
}
func (h *Store) maintain(ctx context.Context, days, maxCount int, dryRun bool) (*State, error) {
	if days < 1 || maxCount < 0 {
		return nil, errors.New("invalid history retention policy")
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	if dryRun {
		unlock, err := lock(ctx, filepath.Join(h.dir, "lock"))
		if err != nil {
			return nil, err
		}
		defer unlock()
		events := []Event{}
		all, err := h.readRetained(ctx, func(e Event) { events = append(events, e) })
		if err != nil {
			return nil, err
		}
		keep := retentionOffset(events, all, cutoff, maxCount)
		return replayEvents(ctx, h.repository, events[keep:])
	}
	if err := os.MkdirAll(h.dir, 0700); err != nil {
		return nil, err
	}
	var state *State
	err := h.accessLog(ctx, true, func(log *wal.Log) error {
		first, err := log.FirstIndex()
		if err != nil {
			return err
		}
		last, err := log.LastIndex()
		if err != nil {
			return err
		}
		events := []Event{}
		for index := first; index <= last; index++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := log.Read(index)
			if err != nil {
				return err
			}
			var event Event
			if err = json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("history entry %d: %w", index, err)
			}
			events = append(events, event)
		}
		all, err := replayEvents(ctx, h.repository, events)
		if err != nil {
			return err
		}
		keep := retentionOffset(events, all, cutoff, maxCount)
		if keep > 0 {
			if err := log.TruncateFront(first + uint64(keep)); err != nil {
				return err
			}
			slog.DebugContext(ctx, "validation history truncated", "repository", h.repository, "events", keep, "cutoff", cutoff)
		}
		state, err = replayEvents(ctx, h.repository, events[keep:])
		if err != nil {
			return err
		}

		retainedDates := map[string]bool{}
		for _, run := range state.Runs {
			retainedDates[run.Start.At.UTC().Format(time.DateOnly)] = true
		}
		for _, kind := range []string{"inputs", "logs"} {
			entries, err := os.ReadDir(filepath.Join(h.dir, kind))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				id := entry.Name()
				if kind == "inputs" {
					id = strings.TrimSuffix(id, ".json")
				}
				date, stream, _ := strings.Cut(id, ".")
				_, dateErr := time.Parse(time.DateOnly, date)
				daily := kind == "logs" && entry.Type().IsRegular() && dateErr == nil && (stream == "stdout.jsonl" || stream == "stderr.jsonl")
				if daily && retainedDates[date] {
					continue
				}
				if !daily && !validID(id) {
					continue
				}
				if kind == "inputs" && state.Inputs[id] || kind == "logs" && state.Runs[id] != nil {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				// Fresh orphan files may belong to a concurrent validation between capture
				// and started/finished. Do not collect them before the retention window.
				if info.ModTime().Before(cutoff) && !dryRun {
					if err := os.RemoveAll(filepath.Join(h.dir, kind, entry.Name())); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return state, err
}

// retentionOffset preserves the newest fixed window; an unfinished older Run may
// pin the prefix, but never displaces a newer retained Run. TTL remains independent.
func retentionOffset(events []Event, all *State, cutoff time.Time, maximum int) int {
	keep := len(events)
	for i, event := range events {
		if !event.At.Before(cutoff) {
			keep = i
			break
		}
	}
	starts := []int{}
	protected := len(events)
	for i, event := range events {
		if i < keep || event.Type != "started" {
			continue
		}
		starts = append(starts, i)
		if all.Runs[event.Run].Result == nil && i < protected {
			protected = i
		}
	}
	if len(starts) > maximum {
		boundary := len(events)
		if maximum > 0 {
			boundary = starts[len(starts)-maximum]
		}
		boundary = min(boundary, protected)
		keep = max(keep, boundary)
	}
	return keep
}
func replayEvents(ctx context.Context, repository string, events []Event) (*State, error) {
	result := newState(repository)
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := result.apply(event); err != nil {
			return nil, err
		}
	}
	return result, nil
}
