package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func atomicFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (h *Store) SaveInputs(id string, data []byte) error {
	return atomicFile(filepath.Join(h.dir, "inputs", id+".json"), data)
}

func (h *Store) LoadInputs(id string) ([]byte, error) {
	if !validID(id) {
		return nil, errors.New("invalid input record ID")
	}
	return os.ReadFile(filepath.Join(h.dir, "inputs", id+".json"))
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

type outputRecord struct {
	RunID  string `json:"runID"`
	Output string `json:"output"`
}

func (h *Store) outputLock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(h.dir, 0700); err != nil {
		return nil, err
	}
	return lock(ctx, filepath.Join(h.dir, "lock"))
}

// +spec=Runs append complete records to two daily streams under the repository lock
// +why=The start's UTC date keeps cross-midnight execution and later reuse on the same files
// +link=component://docs/validation.md
func (h *Store) SaveOutput(ctx context.Context, id string, startedAt time.Time, stdout, stderr string) error {
	if !validID(id) || startedAt.IsZero() {
		return errors.New("invalid run output identity")
	}
	unlock, err := h.outputLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	directory := filepath.Join(h.dir, "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for _, stream := range []struct{ name, text string }{{"stdout", stdout}, {"stderr", stderr}} {
		path := filepath.Join(directory, startedAt.UTC().Format(time.DateOnly)+"."+stream.name+".jsonl")
		if err := appendOutput(path, outputRecord{id, stream.text}); err != nil {
			return err
		}
	}
	return nil
}

func appendOutput(path string, record outputRecord) (err error) {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			return fmt.Errorf("incomplete output record in %s", path)
		}
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func readOutput(ctx context.Context, path, id string) (string, bool, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("read output %s: %w", path, err)
		}
		var record outputRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return "", false, fmt.Errorf("read output %s: %w", path, err)
		}
		if record.RunID == id {
			return record.Output, true, nil
		}
	}
}

// +spec=Legacy pairs remain readable; partial daily output never mixes with legacy streams
func (h *Store) LoadOutput(ctx context.Context, id string, startedAt time.Time) (string, string, error) {
	if !validID(id) || startedAt.IsZero() {
		return "", "", errors.New("invalid run output identity")
	}
	unlock, err := h.outputLock(ctx)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	return h.loadOutput(ctx, id, startedAt)
}

// loadOutput requires the repository lock to cover evidence and its Run together.
func (h *Store) loadOutput(ctx context.Context, id string, startedAt time.Time) (string, string, error) {
	prefix := filepath.Join(h.dir, "logs", startedAt.UTC().Format(time.DateOnly))
	out, foundOut, err := readOutput(ctx, prefix+".stdout.jsonl", id)
	if err != nil {
		return "", "", err
	}
	stderr, foundErr, err := readOutput(ctx, prefix+".stderr.jsonl", id)
	if err != nil {
		return "", "", err
	}
	if foundOut && foundErr {
		return out, stderr, nil
	}
	if foundOut || foundErr {
		return "", "", fmt.Errorf("incomplete daily output for run %s: %w", id, os.ErrNotExist)
	}
	legacyOut, err := os.ReadFile(filepath.Join(h.dir, "logs", id, "stdout"))
	if err != nil {
		return "", "", err
	}
	legacyErr, err := os.ReadFile(filepath.Join(h.dir, "logs", id, "stderr"))
	return string(legacyOut), string(legacyErr), err
}
