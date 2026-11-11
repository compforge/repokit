//go:build linux || darwin

package state

import (
	"context"
	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
	"os"
	"syscall"
)

func repositoryLease(ctx context.Context, path string) (func(), error) {
	return gitworktree.AcquireLease(ctx, path)
}

func directoryIdentity(info os.FileInfo) [2]uint64 {
	stat := info.Sys().(*syscall.Stat_t)
	return [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
}
