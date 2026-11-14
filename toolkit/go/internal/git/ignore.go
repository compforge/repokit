package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/env"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IgnoreMatcher applies incrementally supplied snapshot rules in an isolated
// repository. The zero value has no rules and requires no filesystem resources.
// +spec=`Ignore matching uses only supplied rules, including for tracked paths`
// +why=`Git owns wildcard, directory and negation semantics; parent rules must be available before entering a child directory`
type IgnoreMatcher struct{ root string }

func (m *IgnoreMatcher) Add(ctx context.Context, name string, data []byte) error {
	if !fs.ValidPath(name) || filepath.Base(name) != ".gitignore" {
		return fmt.Errorf("invalid snapshot ignore path %q", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.root == "" {
		root, err := os.MkdirTemp("", "repokit-ignore-*")
		if err != nil {
			return err
		}
		m.root = root
		repo := &Repository{Root: root}
		if _, err := repo.run(ctx, "init", "--quiet", "--template="); err != nil {
			return err
		}
	}
	full := filepath.Join(m.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(full, data, 0600); err != nil {
		return fmt.Errorf("prepare snapshot ignore file %s: %w", name, err)
	}
	return nil
}

// Ignored accepts slash-separated paths; a trailing slash identifies a directory.
func (m *IgnoreMatcher) Ignored(ctx context.Context, paths []string) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ignored := make(map[string]bool)
	if m.root == "" || len(paths) == 0 {
		return ignored, nil
	}
	var input, stderr bytes.Buffer
	queries := make(map[string]string, len(paths))
	for _, name := range paths {
		query := strings.TrimSuffix(name, "/")
		if query != name {
			// Git must observe a directory, but querying its trailing slash can
			// spuriously match a child-only pattern such as tree/*.
			if err := os.MkdirAll(filepath.Join(m.root, filepath.FromSlash(query)), 0700); err != nil {
				return nil, fmt.Errorf("prepare ignore directory %s: %w", query, err)
			}
		}
		queries[query] = name
		input.WriteString(query)
		input.WriteByte(0)
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.excludesFile="+os.DevNull,
		"check-ignore", "--no-index", "--stdin", "-z")
	cmd.Env = env.ForCheckout(os.Environ())
	cmd.Dir, cmd.Stdin, cmd.Stderr = m.root, &input, &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			ignored[queries[name]] = true
		}
	}
	return ignored, nil
}

func (m *IgnoreMatcher) Close() error {
	if m.root == "" {
		return nil
	}
	return os.RemoveAll(m.root)
}
