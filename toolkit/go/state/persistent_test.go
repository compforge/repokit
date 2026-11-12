package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/compforge/repokit/toolkit/go/git/branch"
)

// +case=Repository policy and per-clone branch provenance survive cache expiry; clones cannot read each other's creation facts.
func TestDurableRepositoryFacts(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("REPOKIT_HOME", home)
	root, git := worktreeFixture(t)
	git("remote", "add", "origin", "https://github.com/example/shared.git")
	r, err := CreateBranch(ctx, root, branch.Options{Name: "topic", Base: "HEAD"}, home)
	if err != nil || !r.Confirmed {
		t.Fatal(r, err)
	}
	creation, err := LoadBranchCreation(ctx, root, "topic", home)
	if err != nil || creation == nil || creation.BaseCommit != r.BaseCommit {
		t.Fatal(creation, err)
	}
	policy := WorktreePolicy{MaxCount: 5, MaxPrepared: 2}
	if err = SaveWorktreePolicy(ctx, root, home, policy); err != nil {
		t.Fatal(err)
	}
	second, git2 := worktreeFixture(t)
	git2("remote", "add", "origin", "https://github.com/example/shared.git")
	other, err := LoadBranchCreation(ctx, second, "topic", home)
	if err != nil || other != nil {
		t.Fatal(other, err)
	}
	shared, err := LoadWorktreePolicy(ctx, second, home)
	if err != nil || shared.MaxCount != 5 {
		t.Fatal(shared, err)
	}
	directory, _, _, err := stateLocation(ctx, root, home)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "disposable"), []byte("cache"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		err = withActivity(ctx, directory, func(ctx context.Context, lease *activityLease) error {
			_, e := reclaimRepositoryState(ctx, lease, dry)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		if creation, err = LoadBranchCreation(ctx, root, "topic", home); err != nil || creation == nil {
			t.Fatal(err)
		}
		shared, err = LoadWorktreePolicy(ctx, root, home)
		if err != nil || shared.MaxCount != 5 {
			t.Fatal(shared, err)
		}
	}
	if _, err = os.Stat(filepath.Join(directory, "disposable")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDurablePolicyRejectsNull(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root, _ := worktreeFixture(t)
	directory, err := PersistentDirectory(ctx, root, home)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{`null`, `{"worktree":null}`, `{"worktree":{"max_count":null}}`, `{"worktree":{"outputs":null}}`} {
		if err = os.WriteFile(filepath.Join(directory, "config.json"), []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadWorktreePolicy(ctx, root, home); err == nil {
			t.Fatal("accepted null", document)
		}
	}
}
