//go:build !linux && !darwin

package git

import (
	"context"
	"errors"
)

func AcquireLease(context.Context, string) (func(), error) {
	return nil, errors.New("Git lifecycle operations require POSIX file locking")
}

func TryAcquireLease(ctx context.Context, path string) (func(), error) {
	return AcquireLease(ctx, path)
}
