// Package command executes checkout-bound Git commands and preserves uncertain outcomes.
package command

import (
	"bytes"
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/git/env"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Result retains failed and uncertain outcomes. Cancellation after starting a
// write can leave effects behind; inspect the repository before retrying.
type Result struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Uncertain bool
	Err       error
}

func Run(ctx context.Context, repo string, timeout time.Duration, args ...string) Result {
	return RunIndex(ctx, repo, "", timeout, args...)
}

// RunIndex uses an explicit candidate index while preserving checkout-bound Git execution.
func RunIndex(ctx context.Context, repo, index string, timeout time.Duration, args ...string) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = env.ForCheckout(os.Environ())
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.WaitDelay = time.Second
	gitProcessGroup(cmd)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	// A failed start has no side effect. Once started, cancellation is indeterminate.
	uncertain := cmd.Process != nil && ctx.Err() != nil
	if err != nil {
		err = fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		err = errs.Wrap(err, "git.command_failed", "git."+args[0], repo)
	}
	return Result{ExitCode: code, Stdout: out.String(), Stderr: stderr.String(), Uncertain: uncertain, Err: err}
}
