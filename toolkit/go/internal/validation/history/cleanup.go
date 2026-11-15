package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type CleanupReport struct {
	Before     int  `json:"before"`
	After      int  `json:"after"`
	MaxCount   int  `json:"max_count"`
	OverBudget int  `json:"over_budget"`
	DryRun     bool `json:"dry_run"`
}

func Clean(ctx context.Context, base, key string, days, maxCount int, dryRun bool) (CleanupReport, error) {
	report := CleanupReport{MaxCount: maxCount, DryRun: dryRun}
	if base == "" || key == "" || filepath.Base(key) != key || key == "." || key == ".." || days < 1 || maxCount < 0 {
		return report, errors.New("invalid history cleanup scope or policy")
	}
	h := newStore(base, key, "")
	for _, path := range []string{filepath.Join(base, key), h.dir} {
		info, e := os.Lstat(path)
		if errors.Is(e, os.ErrNotExist) {
			return report, nil
		}
		if e != nil {
			return report, e
		}
		if !info.IsDir() {
			return report, errors.New("history owner must be a real directory")
		}
	}
	info, err := os.Lstat(filepath.Join(h.dir, "history"))
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	if !info.IsDir() {
		return report, errors.New("history must be a real directory")
	}
	before, err := h.ReadRetained(ctx)
	if err != nil {
		return report, err
	}
	report.Before = len(before.Runs)
	after, err := h.maintain(ctx, days, maxCount, dryRun)
	if err != nil {
		return report, err
	}
	report.After = len(after.Runs)
	report.OverBudget = max(0, report.After-maxCount)
	return report, nil
}
