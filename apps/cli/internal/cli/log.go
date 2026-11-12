package cli

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/state"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type commandLog struct {
	logger  *slog.Logger
	file    *os.File
	writer  *logWriter
	started time.Time
	runID   string
}

// slog ignores handler write errors. Surface one warning while preserving the
// command result, including when an already-open log stops accepting writes.
type logWriter struct {
	output io.Writer
	stderr io.Writer
	once   sync.Once
}

func (w *logWriter) warn(err error) {
	w.once.Do(func() { fmt.Fprintln(w.stderr, "repocli: log warning:", err) })
}

func (w *logWriter) Write(data []byte) (int, error) {
	record := append([]byte("[repocli] "), data...)
	// One append write per record keeps concurrent CLI processes from overwriting
	// each other; there is no rename/truncate rotation of the active daily file.
	n, err := w.output.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.warn(err)
		return 0, err
	}
	return len(data), nil
}

func startCommandLog(stderr io.Writer, args []string, explicitHome string) *commandLog {
	now := time.Now()
	run := &commandLog{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), started: now, runID: rand.Text()}
	writer := &logWriter{stderr: stderr}
	home, err := state.ResolveHome(explicitHome)
	if err != nil {
		writer.warn(err)
		return run
	}
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		writer.warn(err)
		return run
	}
	file, err := os.OpenFile(filepath.Join(dir, now.Format("2006-01-02")+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		writer.warn(err)
		return run
	}
	writer.output = file
	run.file, run.writer = file, writer
	run.logger = slog.New(slog.NewTextHandler(writer, nil)).With("run_id", run.runID, "version", Version)
	if err := pruneLogs(dir, now); err != nil {
		writer.warn(err)
	}
	cwd, _ := os.Getwd()
	// JSON preserves argument boundaries so paths with spaces can be replayed.
	argv, _ := json.Marshal(args)
	run.logger.Info("command.started", "cwd", cwd, "args", string(argv))
	return run
}

// Keep today and the preceding 29 local calendar dates. Only our dated regular
// files are eligible: unrelated files, directories and symlinks are left alone.
func pruneLogs(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -29)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		var dateName string
		switch {
		case strings.HasSuffix(name, ".log"):
			dateName = strings.TrimSuffix(name, ".log")
		case strings.HasPrefix(name, "diff-") && strings.HasSuffix(name, ".jsonl"):
			dateName = strings.TrimSuffix(strings.TrimPrefix(name, "diff-"), ".jsonl")
		default:
			continue
		}
		date, err := time.ParseInLocation("2006-01-02", dateName, now.Location())
		if err != nil || !date.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (run *commandLog) finish(command string, code int, err error) {
	fields := []any{"command", command, "elapsed_ms", time.Since(run.started).Milliseconds(), "exit_code", code}
	if err != nil {
		run.logger.Error("command.failed", fields...)
	} else {
		run.logger.Info("command.finished", fields...)
	}
}

func (run *commandLog) close() {
	if run.file != nil {
		if err := run.file.Close(); err != nil {
			run.writer.warn(err)
		}
	}
}
