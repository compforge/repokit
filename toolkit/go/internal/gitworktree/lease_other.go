//go:build !linux && !darwin

package gitworktree

import (
	"context"
	"errors"
)

func AcquireLease(context.Context, string) (func(), error) {
	return nil, errors.New("Git lifecycle operations require POSIX file locking")
}
