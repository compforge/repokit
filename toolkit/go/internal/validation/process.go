package validation

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type checkOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
	stop     context.CancelFunc
}

func (b *checkOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		b.Buffer.Write(p[:remaining])
		b.overflow = true
		b.stop()
		return n, nil
	}
	b.Buffer.Write(p)
	return n, nil
}

func executeMake(ctx context.Context, cwd string, spec CheckSpec, environment []string) CheckResult {
	return executeMakeStarted(ctx, cwd, spec, environment, nil)
}

func executeMakeStarted(ctx context.Context, cwd string, spec CheckSpec, environment []string, onStart func() string) CheckResult {
	result := CheckResult{CheckSpec: spec, Status: "unavailable"}
	if err := ctx.Err(); err != nil {
		result.Status = "cancelled"
		result.Diagnostic = err.Error()
		return result
	}
	child, stop := context.WithCancel(ctx)
	defer stop()
	// Resolve make from the environment actually passed to the check, not the
	// host process's PATH. This also makes explicit environments reproducible.
	path := ""
	for _, item := range environment {
		if value, ok := strings.CutPrefix(item, "PATH="); ok {
			path = value
		}
	}
	executable, err := validationExecutable(path, cwd)
	if err != nil {
		result.Diagnostic = err.Error()
		return result
	}
	cmd := exec.CommandContext(child, executable, spec.Command[1:]...)
	cmd.Dir = cwd
	cmd.Env = environment
	cmd.WaitDelay = time.Second
	cleanup, err := validationProcessGroup(cmd)
	if err != nil {
		result.Diagnostic = err.Error()
		return result
	}
	defer cleanup()
	out := &checkOutput{limit: 16 << 20, stop: stop}
	stderr := &checkOutput{limit: 64 << 10, stop: stop}
	cmd.Stdout = out
	cmd.Stderr = stderr
	started := time.Now()
	slog.InfoContext(ctx, "validation check started", "component", cwd, "check", spec.Check, "scope", spec.Scope)
	err = cmd.Start()
	if err == nil {
		if onStart != nil {
			result.RunID = onStart()
		}
		err = cmd.Wait()
	}
	result.Stdout = out.String()
	result.Stderr = stderr.String()
	if spec.Check == "test" {
		result.TestTimings = goTestTimings(result.Stdout)
	}
	switch {
	case cmd.Process == nil:
		result.Diagnostic = err.Error()
	case out.overflow || stderr.overflow:
		result.Status = "uncertain"
		result.Diagnostic = "check output exceeded budget; process group terminated"
	case ctx.Err() != nil:
		result.Status = "uncertain"
		result.Diagnostic = ctx.Err().Error()
	default:
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
		result.Status = "passed"
		if err != nil {
			result.Status = "failed"
			result.Diagnostic = err.Error()
		}
	}
	slog.InfoContext(ctx, "validation check finished", "component", cwd, "check", spec.Check, "status", result.Status, "duration_seconds", time.Since(started).Seconds(), "test_timings", len(result.TestTimings))
	return result
}

func validationExecutable(path, cwd string) (string, error) {
	for _, directory := range filepath.SplitList(path) {
		if directory == "" {
			directory = cwd
		}
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(cwd, directory)
		}
		candidate := filepath.Join(directory, "make")
		resolved, err := exec.LookPath(candidate)
		if err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("make is not executable in the check environment PATH")
}
