package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Summaries contains retained histories and identifies stores that could not be read.
type Summaries struct {
	Repositories []Summary `json:"repositories"`
	Diagnostics  []string  `json:"diagnostics"`
}

// ReadSummaries reads existing repository stores without needing live checkouts.
// +spec=Reuse the WAL projection and locking; never maintain retention or count a corrupt store partially
// +link=../../../../../docs/validation.md
func ReadSummaries(ctx context.Context, directory string) (Summaries, error) {
	result := Summaries{Repositories: []Summary{}, Diagnostics: []string{}}
	if directory == "" {
		return result, errors.New("history directory is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(directory, entry.Name(), "validation", "history")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // PR observation caches share this root.
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("not a regular history directory")
		}
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		state, err := newStore(directory, entry.Name(), "").Read(ctx)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		result.Repositories = append(result.Repositories, state.Summary)
	}
	return result, ctx.Err()
}
