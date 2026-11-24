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
}

// NewRefReader selects the SDK when Git reports a supported object and ref format.
func NewRefReader(ctx context.Context, checkout worktree.CheckoutInfo) RefReader {
	native := process.RefReader(checkout.Root)
	formats, err := native.Formats(ctx)
	if err == nil {
		if sdk := gogit.OpenRefReader(checkout, formats); sdk != nil {
			return fallbackRefReader{primary: sdk, fallback: native}
		}
	}
	slog.DebugContext(ctx, "Using native Git for branch reads", "checkout", checkout.Root, "formats", formats)
	return native
}

type fallbackRefReader struct {
	primary  RefReader
	fallback RefReader
}

func (reader fallbackRefReader) BranchCommit(ctx context.Context, branch string) (string, bool, error) {
	sha, found, err := reader.primary.BranchCommit(ctx, branch)
	if errors.Is(err, errors.ErrUnsupported) {
		if canceled := ctx.Err(); canceled != nil {
			return "", false, canceled
		}
		return reader.fallback.BranchCommit(ctx, branch)
	}
	return sha, found, err
}
