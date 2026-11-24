package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/gogit"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
)

// +case=Both readers observe packed and loose branch tips moving in ordinary, linked and shared checkouts, and preserve native absence and error semantics.
func TestRefReaderContract(t *testing.T) {
	ctx := context.Background()
	for _, layout := range []string{"ordinary", "linked", "alternates"} {
		t.Run(layout, func(t *testing.T) {
			origin := fixture(t)
			first := strings.TrimSpace(gitCommand(t, origin, "rev-parse", "HEAD"))
			gitCommand(t, origin, "commit", "--allow-empty", "-qm", "second")
			second := strings.TrimSpace(gitCommand(t, origin, "rev-parse", "HEAD"))
			root := origin
			switch layout {
			case "linked":
				root = filepath.Join(t.TempDir(), "linked")
				gitCommand(t, origin, "worktree", "add", "--detach", root, second)
			case "alternates":
				root = filepath.Join(t.TempDir(), "shared")
				gitCommand(t, origin, "clone", "--shared", origin, root)
			}
			gitCommand(t, root, "branch", "observed", first)
			gitCommand(t, root, "pack-refs", "--all")
			info, err := worktree.Local(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			reader := git.NewRefReader(ctx, info)
			native := func(ctx context.Context, branch string) (string, bool, error) {
				return git.BranchCommit(ctx, root, branch)
			}
			check := func(branch, want string, exists bool) {
				t.Helper()
				for _, read := range []func(context.Context, string) (string, bool, error){native, reader.BranchCommit} {
					got, found, err := read(ctx, branch)
					if err != nil || got != want || found != exists {
						t.Fatalf("read %s: sha=%q found=%t err=%v", branch, got, found, err)
					}
				}
			}
			check("observed", first, true)
			heads := filepath.Join(info.CommonDir, "refs", "heads")
			put(t, heads, "observed", first+"x")
			check("observed", "", false)
			if err := os.Remove(filepath.Join(heads, "observed")); err != nil {
				t.Fatal(err)
			}
			gitCommand(t, root, "update-ref", "refs/heads/observed", second, first)
			check("observed", second, true)
			if strings.TrimSpace(gitCommand(t, root, "rev-parse", "--show-object-format", "--show-ref-format")) == "sha1\nfiles" {
				t.Run("loose_tip_without_subprocess", func(t *testing.T) {
					trace := filepath.Join(t.TempDir(), "trace")
					t.Setenv("GIT_TRACE2_EVENT", trace)
					for i := 0; i < 3; i++ {
						got, found, err := reader.BranchCommit(ctx, "observed")
						if err != nil || !found || got != second {
							t.Fatal(got, found, err)
						}
					}
					if data, err := os.ReadFile(trace); len(data) > 0 || (err != nil && !os.IsNotExist(err)) {
						t.Fatalf("SDK reads launched Git: %v", err)
					}
				})
			}
			gitCommand(t, root, "update-ref", "-d", "refs/heads/observed", second)
			check("observed", "", false)
			gitCommand(t, root, "tag", "-a", "-m", "peel", "peel", second)
			put(t, heads, "tag-tip", gitCommand(t, root, "rev-parse", "refs/tags/peel"))
			check("tag-tip", second, true)
			put(t, heads, "broken", strings.Repeat("e", 40)+"\n")
			_, wantFound, wantErr := native(ctx, "broken")
			_, found, gotErr := reader.BranchCommit(ctx, "broken")
			if wantErr == nil || gotErr == nil || found != wantFound {
				t.Fatalf("broken: sdk=%t %v, native=%t %v", found, gotErr, wantFound, wantErr)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, _, err := reader.BranchCommit(canceled, "main"); err == nil {
				t.Fatal("canceled query succeeded")
			}
		})
	}
}

func TestRefReaderOtherFormats(t *testing.T) {
	for _, flag := range []string{"--object-format=sha256", "--ref-format=reftable"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			if output, err := exec.Command("git", "init", "-q", "-b", "main", flag, root).CombinedOutput(); err != nil {
				t.Skipf("Git does not support %s: %s", flag, output)
			}
			gitCommand(t, root, "commit", "--allow-empty", "-qm", "fixture")
			info, err := worktree.Local(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := git.NewRefReader(context.Background(), info).BranchCommit(context.Background(), "main")
			if err != nil || !found || got != strings.TrimSpace(gitCommand(t, root, "rev-parse", "HEAD")) {
				t.Fatal(got, found, err)
			}
		})
	}
}

// +case=The SDK reports unsupported evidence to its caller and never launches native Git itself.
func TestSDKRefReaderDoesNotFallback(t *testing.T) {
	ctx := context.Background()
	root := fixture(t)
	info, err := worktree.Local(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	formats, err := process.RefReader(root).Formats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := gogit.OpenRefReader(info, formats)
	if reader == nil {
		t.Skip("repository format is not supported by the SDK")
	}
	composed := git.NewRefReader(ctx, info)
	trace := filepath.Join(t.TempDir(), "git-trace")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	if _, _, err := reader.BranchCommit(ctx, "missing"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("missing ref: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := composed.BranchCommit(canceled, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
	if data, err := os.ReadFile(trace); len(data) != 0 || (err != nil && !os.IsNotExist(err)) {
		t.Fatalf("unexpected Git process: %v", err)
	}
}
