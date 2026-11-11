//go:build !linux && !darwin

package state

import (
	"context"
	"errors"
	"os"
)

func repositoryLease(context.Context, string) (func(), error) {
	return nil, errors.New("worktree maintenance requires POSIX file locking")
}
func directoryIdentity(os.FileInfo) [2]uint64 { return [2]uint64{} }
