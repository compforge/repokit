package depot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/gitenv"
)

type output struct {
	bytes.Buffer
	limit    int
	overflow bool
	stop     context.CancelFunc
}

func (b *output) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if n > remaining {
		b.Buffer.Write(p[:remaining])
		b.overflow = true
		b.stop()
		return n, nil
	}
	return b.Buffer.Write(p)
}

type execution struct {
	started, uncertain bool
	code               *int
	stdout, stderr     string
	err                error
}

func run(ctx context.Context, root string, args []string, manager string) execution {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	child, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(child, args[0], args[1:]...)
	cmd.Dir = root
	cmd.WaitDelay = time.Second
	cmd.Env = gitenv.ForCheckout(os.Environ())
	if manager == "uv" {
		env := []string{}
		for _, v := range cmd.Env {
			if !strings.HasPrefix(v, "VIRTUAL_ENV=") && !strings.HasPrefix(v, "UV_PROJECT_ENVIRONMENT=") {
				env = append(env, v)
			}
		}
		cmd.Env = env
	}
	cleanup, err := processGroup(cmd)
	if err != nil {
		return execution{err: err}
	}
	defer cleanup()
	out, stderr := &output{limit: 4 << 20, stop: stop}, &output{limit: 64 << 10, stop: stop}
	cmd.Stdout, cmd.Stderr = out, stderr
	err = cmd.Start()
	started := err == nil
	if started {
		err = cmd.Wait()
	}
	result := execution{started: started, stdout: out.String(), stderr: stderr.String(), err: err}
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		result.code = &code
	}
	if started && (ctx.Err() != nil || out.overflow || stderr.overflow) {
		result.uncertain = true
		result.err = ctx.Err()
		if result.err == nil {
			result.err = errors.New("installer output exceeded budget; process group terminated")
		}
	}
	return result
}
