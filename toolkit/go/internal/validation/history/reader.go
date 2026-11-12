package history

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var segmentPattern = regexp.MustCompile(`^[0-9]{20}(?:\.START|\.END)?$`)

// readRetained replays the shared JSON WAL without opening a writable wal.Log.
// +spec=Read-only queries apply logical truncation recovery without creating, renaming or removing segments.
// +link=component://docs/validation.md
func (h *Store) readRetained(ctx context.Context) (*State, error) {
	entries, err := os.ReadDir(filepath.Join(h.dir, "history"))
	if err != nil {
		return nil, err
	}
	segments := []string{}
	start, end := -1, -1
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !segmentPattern.MatchString(name) {
			continue
		}
		index, err := strconv.ParseUint(name[:20], 10, 64)
		if err != nil || index == 0 {
			continue
		}
		if strings.HasSuffix(name, ".START") {
			if start != -1 {
				return nil, errors.New("multiple history START segments")
			}
			start = len(segments)
		}
		if strings.HasSuffix(name, ".END") {
			if end != -1 {
				return nil, errors.New("multiple history END segments")
			}
			end = len(segments)
		}
		segments = append(segments, name)
	}
	if start != -1 && end != -1 {
		return nil, errors.New("conflicting history recovery segments")
	}
	if start != -1 {
		segments = segments[start:]
	}
	if end != -1 {
		segments = segments[:end+1]
		if end > 0 && segments[end-1][:20] == segments[end][:20] {
			segments[end-1] = segments[end]
			segments = segments[:end]
		}
	}
	state := newState(h.repository)
	var expected uint64
	for i, name := range segments {
		index, _ := strconv.ParseUint(name[:20], 10, 64)
		if i > 0 && index != expected {
			return nil, errors.New("noncontiguous validation history segments")
		}
		count, err := readSegment(ctx, filepath.Join(h.dir, "history", name), state)
		if err != nil {
			return nil, err
		}
		expected = index + count
	}
	return state, nil
}

func readSegment(ctx context.Context, path string, state *State) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var count uint64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			return count, nil
		}
		if err != nil {
			return 0, errors.New("incomplete validation history record")
		}
		var record struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return 0, err
		}
		if len(record.Data) == 0 {
			return 0, errors.New("invalid validation WAL payload")
		}
		var data []byte
		switch record.Data[0] {
		case '+':
			data = []byte(record.Data[1:])
		case '$':
			data, err = base64.URLEncoding.DecodeString(record.Data[1:])
			if err != nil {
				return 0, err
			}
		default:
			return 0, errors.New("invalid validation WAL encoding")
		}
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			return 0, err
		}
		if err := state.apply(event); err != nil {
			return 0, err
		}
		count++
	}
}
