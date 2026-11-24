package git

import (
	"context"
	"errors"
	"log/slog"

	"github.com/compforge/repokit/toolkit/go/internal/git/gogit"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
)

// RefReader is bound to one checkout. Each call observes the branch again.
type RefReader interface {
	BranchCommit(ctx context.Context, branch string) (sha string, found bool, err error)
	// AttachedHead returns the symbolic ref and its commit. An unborn branch has an empty SHA.
	AttachedHead(ctx context.Context) (ref, sha string, err error)
}

// Reader also observes the tree and ordered parents of a known commit.
type Reader interface {
	RefReader
	CommitMetadata(ctx context.Context, sha string) (tree string, parents []string, err error)
}

// NewReader selects the SDK when Git reports a supported object and ref format.
func NewReader(ctx context.Context, checkout worktree.CheckoutInfo) Reader {
	native := process.RefReader(checkout.Root)
	formats, err := native.Formats(ctx)
	if err == nil {
		if sdk := gogit.OpenRefReader(checkout.GitDir, checkout.CommonDir, formats); sdk != nil {
			return fallbackReader{primary: sdk, fallback: native}
		}
	}
	slog.DebugContext(ctx, "Using native Git for checkout reads", "checkout", checkout.Root, "formats", formats)
	return native
}

type fallbackReader struct {
	primary  Reader
	fallback Reader
}

func (reader fallbackReader) BranchCommit(ctx context.Context, branch string) (string, bool, error) {
	sha, found, err := reader.primary.BranchCommit(ctx, branch)
	if errors.Is(err, errors.ErrUnsupported) {
		if canceled := ctx.Err(); canceled != nil {
			return "", false, canceled
		}
		return reader.fallback.BranchCommit(ctx, branch)
	}
	return sha, found, err
}

func (reader fallbackReader) AttachedHead(ctx context.Context) (string, string, error) {
	ref, sha, err := reader.primary.AttachedHead(ctx)
	if errors.Is(err, errors.ErrUnsupported) {
		return reader.fallback.AttachedHead(ctx)
	}
	return ref, sha, err
}

func (reader fallbackReader) CommitMetadata(ctx context.Context, sha string) (string, []string, error) {
	tree, parents, err := reader.primary.CommitMetadata(ctx, sha)
	if errors.Is(err, errors.ErrUnsupported) {
		return reader.fallback.CommitMetadata(ctx, sha)
	}
	return tree, parents, err
}
