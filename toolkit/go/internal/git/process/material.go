package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/git/env"
)

// Material provides native Git output for repository snapshots and comparisons.
type Material string

func (r Material) TopLevel(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--show-toplevel")
	return strings.TrimSuffix(string(out), "\n"), err
}

func (r Material) Entries(ctx context.Context, head string) ([]byte, error) {
	if head != "" {
		return r.Tree(ctx, head)
	}
	return r.run(ctx, "ls-files", "--stage", "-z")
}

func (r Material) Tree(ctx context.Context, ref string) ([]byte, error) {
	return r.run(ctx, "ls-tree", "-r", "-z", "--full-tree", ref)
}

func (r Material) Untracked(ctx context.Context) ([]byte, error) {
	return r.run(ctx, "ls-files", "-z", "--others", "--exclude-standard")
}

func (r Material) Origin(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "config", "--get", "remote.origin.url")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), err
}

// CommonDir identifies the shared metadata directory across linked worktrees.
func (r Material) CommonDir(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(string(out), "\n"))
}

func (r Material) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Env = env.ForCheckout(os.Environ())
	cmd.Dir = string(r)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (r Material) Resolve(ctx context.Context, ref string) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(string(out)), err
}

func (r Material) Patch(ctx context.Context, base string) ([]byte, error) {
	return r.run(ctx, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--find-renames", base, "--")
}

// ComparisonPatch uses resolved object IDs; user refs never become Git options.
func (r Material) ComparisonPatch(ctx context.Context, base, head string, staged bool) ([]byte, error) {
	if base == "" {
		// Git's empty-tree identity depends on the repository object format. Computing
		// it does not write an object or require the checkout to have a first commit.
		data, err := r.run(ctx, "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return nil, err
		}
		base = strings.TrimSpace(string(data))
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--binary", "--find-renames"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, base)
	if head != "" {
		args = append(args, head)
	}
	return r.run(ctx, append(args, "--")...)
}
