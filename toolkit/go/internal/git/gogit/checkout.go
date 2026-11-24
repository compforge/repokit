package gogit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// AttachedHead uses the worktree-local HEAD and the shared branch store.
// Unusual HEAD representations and symbolic chains retain Git's native semantics.
func (reader *RefReader) AttachedHead(ctx context.Context) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	path := filepath.Join(reader.gitDir, "HEAD")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", "", errors.ErrUnsupported
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", errors.ErrUnsupported
	}
	ref, ok := strings.CutPrefix(strings.TrimSuffix(string(raw), "\n"), "ref: refs/heads/")
	if !ok || !ValidBranchName(ref) {
		return "", "", errors.ErrUnsupported
	}
	sha, _, err := reader.BranchCommit(ctx, ref)
	return "refs/heads/" + ref, sha, err
}

// CommitMetadata observes the effective tree and parents. Git handles
// history overrides, including shallow boundaries, grafts and replacement refs.
func (reader *RefReader) CommitMetadata(ctx context.Context, sha string) (string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	hash := plumbing.NewHash(sha)
	if hash.String() != sha {
		return "", nil, errors.ErrUnsupported
	}
	for _, name := range []string{"shallow", "info/grafts"} {
		if _, err := os.Lstat(filepath.Join(reader.commonDir, name)); !errors.Is(err, os.ErrNotExist) {
			return "", nil, errors.ErrUnsupported
		}
	}
	commit, err := reader.ordinaryCommit(hash)
	if err != nil {
		return "", nil, err
	}
	parents := make([]string, len(commit.ParentHashes))
	for i, parent := range commit.ParentHashes {
		// Native git show parses immediate parents even with --no-patch.
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		if _, err := reader.ordinaryCommit(parent); err != nil {
			return "", nil, err
		}
		parents[i] = parent.String()
	}
	return commit.TreeHash.String(), parents, ctx.Err()
}

// The SDK decodes the whole message. Delegate unusually large commits to Git.
func (reader *RefReader) ordinaryCommit(hash plumbing.Hash) (*object.Commit, error) {
	if _, err := reader.store.Reference(plumbing.ReferenceName("refs/replace/" + hash.String())); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, errors.ErrUnsupported
	}
	encoded, err := reader.store.EncodedObject(plumbing.CommitObject, hash)
	if err != nil || encoded.Size() > 1<<20 {
		return nil, errors.ErrUnsupported
	}
	commit, err := object.DecodeCommit(reader.store, encoded)
	if err != nil {
		return nil, errors.ErrUnsupported
	}
	return commit, nil
}
