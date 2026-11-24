package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	git "github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
)

func TestBranchNameParity(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"topic", "feat/中文", "@", "team/@/one", "ok/-dash", "", "HEAD", "-dash", "a.lock", "a..b", "a//b", "a@{b", "a~b", ".hidden", "a\x7fb"} {
		want := name != "" && name != "HEAD" && !strings.HasPrefix(name, "-") &&
			exec.Command("git", "check-ref-format", "refs/heads/"+name).Run() == nil
		if err := git.ValidateBranch(context.Background(), root, name); (err == nil) != want {
			t.Errorf("%q: native valid=%v, got %v", name, want, err)
		}
	}
}

func TestCheckoutReader(t *testing.T) {
	ctx := context.Background()
	root := fixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitCommand(t, root, "worktree", "add", "-b", "topic", linked, "HEAD")
	info, err := worktree.Local(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	reader := git.NewReader(ctx, info)
	native := process.RefReader(linked)
	checkHead := func() {
		t.Helper()
		wantRef, wantSHA, wantErr := native.AttachedHead(ctx)
		ref, sha, err := reader.AttachedHead(ctx)
		if ref != wantRef || sha != wantSHA || (err == nil) != (wantErr == nil) {
			t.Fatalf("HEAD: got %q %q %v, want %q %q %v", ref, sha, err, wantRef, wantSHA, wantErr)
		}
	}
	checkCommit := func(sha string) {
		t.Helper()
		tree, parents, err := reader.CommitMetadata(ctx, sha)
		wantTree, wantParents, wantErr := native.CommitMetadata(ctx, sha)
		if err != nil || wantErr != nil || tree != wantTree || !slices.Equal(parents, wantParents) {
			t.Fatalf("commit %s: got %s %v %v, want %s %v %v", sha, tree, parents, err, wantTree, wantParents, wantErr)
		}
	}
	head := func() string { return strings.TrimSpace(gitCommand(t, linked, "rev-parse", "HEAD")) }
	first := head()
	checkHead()
	checkCommit(first)
	gitCommand(t, linked, "commit", "--allow-empty", "-qm", "topic change")
	gitCommand(t, root, "commit", "--allow-empty", "-qm", "main change")
	gitCommand(t, linked, "merge", "--no-edit", "main")
	merge := head()
	checkHead()
	checkCommit(merge)
	if formats, _ := native.Formats(ctx); formats == "sha1\nfiles" {
		t.Run("ordinary_queries_without_subprocess", func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("GIT_TRACE2_EVENT", trace)
			if err := git.ValidateBranch(ctx, linked, "topic/a"); err != nil {
				t.Fatal(err)
			}
			if ref, sha, err := reader.AttachedHead(ctx); err != nil || ref != "refs/heads/topic" || sha != merge {
				t.Fatal(ref, sha, err)
			}
			if _, parents, err := reader.CommitMetadata(ctx, merge); err != nil || len(parents) != 2 {
				t.Fatal(parents, err)
			}
			if data, err := os.ReadFile(trace); len(data) > 0 || !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("git trace %s: %v", data, err)
			}
		})
	}
	gitCommand(t, linked, "replace", merge, first)
	checkCommit(merge)
	gitCommand(t, linked, "replace", "-d", merge)
	put(t, info.CommonDir, "shallow", merge+"\n")
	checkCommit(merge)
	if err := os.Remove(filepath.Join(info.CommonDir, "shallow")); err != nil {
		t.Fatal(err)
	}
	put(t, info.CommonDir, "info/grafts", merge+"\n")
	checkCommit(merge)
	gitCommand(t, linked, "switch", "--detach", merge)
	checkHead()
	gitCommand(t, linked, "symbolic-ref", "HEAD", "refs/heads/not-created")
	checkHead()
	gitCommand(t, linked, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	gitCommand(t, linked, "symbolic-ref", "HEAD", "refs/heads/alias")
	checkHead()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := reader.AttachedHead(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := reader.CommitMetadata(canceled, merge); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// +case=Commit verification preserves native failure when a declared parent cannot be read.
func TestCommitMetadataMissingParent(t *testing.T) {
	ctx := context.Background()
	root := fixture(t)
	parent := strings.TrimSpace(gitCommand(t, root, "rev-parse", "HEAD"))
	gitCommand(t, root, "commit", "--allow-empty", "-qm", "child")
	tip := strings.TrimSpace(gitCommand(t, root, "rev-parse", "HEAD"))
	info, err := worktree.Local(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(info.CommonDir, "objects", parent[:2], parent[2:])); err != nil {
		t.Fatal(err)
	}
	for _, reader := range []git.Reader{git.NewReader(ctx, info), process.RefReader(root)} {
		if _, _, err := reader.CommitMetadata(ctx, tip); err == nil {
			t.Fatal("missing parent accepted")
		}
	}
}
