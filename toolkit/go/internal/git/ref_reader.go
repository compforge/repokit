package git

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// RefReader is bound to one checkout. Each call observes the branch again.
type RefReader interface {
	BranchCommit(ctx context.Context, branch string) (sha string, found bool, err error)
}

type sdkRefReader struct {
	store     *filesystem.Storage
	native    processRefReader
	commonDir string
}

// NewRefReader selects the local SDK for the ref and object formats it understands.
// Older Git versions may not report the ref format. Native Git remains authoritative in that case.
func NewRefReader(ctx context.Context, checkout worktree.CheckoutInfo) RefReader {
	native := processRefReader(checkout.Root)
	formats := command.Run(ctx, checkout.Root, 5*time.Second, "rev-parse", "--show-object-format", "--show-ref-format")
	if len(plumbing.ZeroHash) != 20 || formats.Err != nil || strings.TrimSpace(formats.Stdout) != "sha1\nfiles" {
		slog.DebugContext(ctx, "Using native Git for branch reads", "checkout", checkout.Root, "formats", strings.TrimSpace(formats.Stdout))
		return native
	}
	fs := dotgit.NewRepositoryFilesystem(osfs.New(checkout.GitDir), osfs.New(checkout.CommonDir))
	store := filesystem.NewStorage(fs, cache.NewObjectLRU(0))
	return &sdkRefReader{store: store, native: native, commonDir: checkout.CommonDir}
}

// +spec=Source checks observe the current ref even if an external process moves it after preflight. Only an ordinary ref naming a commit is answered directly.
// +link=component://docs/git-push.md
func (reader *sdkRefReader) BranchCommit(ctx context.Context, branch string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	name := plumbing.NewBranchReferenceName(branch)
	if name.Validate() != nil {
		return reader.native.BranchCommit(ctx, branch)
	}
	// go-git may normalize malformed SHA text or return an old packed value when
	// a loose ref is unreadable. Only use the SDK for a canonical loose branch;
	// the process reader handles packed-only refs and unusual representations.
	path := filepath.Join(reader.commonDir, filepath.FromSlash(name.String()))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (info.Size() != 40 && info.Size() != 41) {
		return reader.native.BranchCommit(ctx, branch)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return reader.native.BranchCommit(ctx, branch)
	}
	ref, err := reader.store.Reference(name)
	if err != nil || ref.Type() != plumbing.HashReference || ref.Hash().IsZero() || ref.Hash().String() != strings.TrimSuffix(string(raw), "\n") {
		return reader.native.BranchCommit(ctx, branch)
	}
	hash := ref.Hash()
	_, err = reader.store.Reference(plumbing.ReferenceName("refs/replace/" + hash.String()))
	if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return reader.native.BranchCommit(ctx, branch)
	}
	if !reader.hasCommit(hash) {
		// Git also understands peeled tags, partial clones and newly repacked object stores.
		return reader.native.BranchCommit(ctx, branch)
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return hash.String(), true, nil
}

// Limit SDK object lookup to the checkout and its immediate alternates. Git
// handles more complex chains and missing objects with its native traversal.
func (reader *sdkRefReader) hasCommit(hash plumbing.Hash) bool {
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
