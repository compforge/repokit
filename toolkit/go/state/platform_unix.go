//go:build linux || darwin

package state

import (
	"context"
	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
	"os"
	"syscall"
)

func repositoryLease(ctx context.Context, path string) (func(), error) {
	return repogit.AcquireLease(ctx, path)
}

func directoryIdentity(info os.FileInfo) [2]uint64 {
	stat := info.Sys().(*syscall.Stat_t)
	return [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
}
