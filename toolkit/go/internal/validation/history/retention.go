package history

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/wal"
)

// Maintain truncates expired events regardless of Run status or references.
// +spec=Unfinished Runs and recent reuses never pin the expired history prefix
// +why=History is disposable evidence; losing a Run requires execution instead of unbounded retention
// +link=component://docs/validation.md
func (h *Store) Maintain(ctx context.Context, days int) (*State, error) {
	var state *State
	err := h.access(ctx, func(log *wal.Log) error {
		first, err := log.FirstIndex()
		if err != nil {
			return err
		}
		last, err := log.LastIndex()
		if err != nil {
			return err
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		keep := last + 1
		for index := first; index <= last; index++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := log.Read(index)
			if err != nil {
				return err
			}
			var event Event
			if err := json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("history entry %d: %w", index, err)
			}
			if !event.At.Before(cutoff) {
				keep = index
				break
			}
		}
		if keep > first {
			if err := log.TruncateFront(keep); err != nil {
				return err
			}
			slog.DebugContext(ctx, "validation history truncated", "repository", h.repository, "events", keep-first, "cutoff", cutoff)
		}
		state, err = h.replay(ctx, log)
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
				if info.ModTime().Before(cutoff) {
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
