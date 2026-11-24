// Package gogit implements checkout-bound queries using the go-git SDK.
package gogit

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// RefReader returns errors.ErrUnsupported when the SDK cannot establish a branch tip.
type RefReader struct {
	store     *filesystem.Storage
	commonDir string
}

// OpenRefReader returns nil for object or ref formats the SDK cannot read.
func OpenRefReader(checkout worktree.CheckoutInfo, formats string) *RefReader {
	if len(plumbing.ZeroHash) != 20 || formats != "sha1\nfiles" {
		return nil
	}
	fs := dotgit.NewRepositoryFilesystem(osfs.New(checkout.GitDir), osfs.New(checkout.CommonDir))
	store := filesystem.NewStorage(fs, cache.NewObjectLRU(0))
	return &RefReader{store: store, commonDir: checkout.CommonDir}
}

// +spec=Source checks observe the current ref even if an external process moves it after preflight. Only an ordinary ref naming a commit is answered directly.
// +link=component://docs/git-push.md
func (reader *RefReader) BranchCommit(ctx context.Context, branch string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	name := plumbing.NewBranchReferenceName(branch)
	if name.Validate() != nil {
		return "", false, errors.ErrUnsupported
	}
	// go-git may normalize malformed SHA text or return an old packed value when
	// a loose ref is unreadable. Only use the SDK for a canonical loose branch;
	// the process reader handles packed-only refs and unusual representations.
	path := filepath.Join(reader.commonDir, filepath.FromSlash(name.String()))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (info.Size() != 40 && info.Size() != 41) {
		return "", false, errors.ErrUnsupported
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, errors.ErrUnsupported
	}
	ref, err := reader.store.Reference(name)
	if err != nil || ref.Type() != plumbing.HashReference || ref.Hash().IsZero() || ref.Hash().String() != strings.TrimSuffix(string(raw), "\n") {
		return "", false, errors.ErrUnsupported
	}
	hash := ref.Hash()
	_, err = reader.store.Reference(plumbing.ReferenceName("refs/replace/" + hash.String()))
	if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", false, errors.ErrUnsupported
	}
	if !reader.hasCommit(hash) {
		// Git also understands peeled tags, partial clones and newly repacked object stores.
		return "", false, errors.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return hash.String(), true, nil
}

// Limit SDK object lookup to the checkout and its immediate alternates. Git
// handles more complex chains and missing objects with its native traversal.
func (reader *RefReader) hasCommit(hash plumbing.Hash) bool {
	if directCommit(&reader.store.ObjectStorage, hash) {
		return true
	}
	alternates, err := os.Open(filepath.Join(reader.commonDir, "objects", "info", "alternates"))
	if err != nil {
		return false
	}
	defer alternates.Close()
	lines := bufio.NewScanner(alternates)
	for lines.Scan() {
		objects := lines.Text()
		// Restrict the SDK to plain absolute paths naming a standard objects directory.
		// Quoted paths, relative paths and custom object directory names go through Git.
		if !filepath.IsAbs(objects) || filepath.Base(objects) != "objects" {
			return false
		}
		dir := dotgit.New(osfs.New(filepath.Dir(objects)))
		if directCommit(filesystem.NewObjectStorage(dir, cache.NewObjectLRU(0)), hash) {
			return true
		}
	}
	return false
}

func directCommit(store *filesystem.ObjectStorage, hash plumbing.Hash) bool {
	if err := store.HasEncodedObject(hash); err != nil {
		return false
	}
	_, err := store.EncodedObject(plumbing.CommitObject, hash)
	return err == nil
}
