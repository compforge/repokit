package repostate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Read distinguishes missing observations from corrupt or unreadable observations.
func Read(path string, value any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = json.Unmarshal(data, value); err != nil {
		return false, fmt.Errorf("read repository state %s: %w", path, err)
	}
	return true, nil
}

// Write atomically publishes a private record while the caller owns its refresh lease.
func Write(path string, value any, owned func() error) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".observation-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = owned(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// WithLock implements the existing Python/proper-lockfile record lease: 30s stale,
// 10s renewal, an empty .lock directory. Network and Git work use the same deadline.
// +spec=`All language implementations serialize refresh on the same record path; cancellation or lease loss forbids publication.`
func WithLock(ctx context.Context, path string, fn func(func() error) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock := path + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := os.Mkdir(lock, 0700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Stat(lock)
		if statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lock)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	original, err := os.Stat(lock)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var compromised error
	stamp := original.ModTime()
	check := func() error {
		if compromised != nil {
			return compromised
		}
		current, err := os.Stat(lock)
		if err != nil {
			return err
		}
		if !os.SameFile(original, current) || !current.ModTime().Equal(stamp) || time.Since(stamp) > 30*time.Second {
			return errors.New("repository state lease replaced or expired")
		}
		return nil
	}
	owned := func() error {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return check()
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mu.Lock()
				compromised = check()
				if compromised == nil {
					now := time.Now()
					compromised = os.Chtimes(lock, now, now)
					if compromised == nil {
						var current os.FileInfo
						current, compromised = os.Stat(lock)
						if compromised == nil {
							stamp = current.ModTime()
						}
					}
				}
				mu.Unlock()
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
		mu.Lock()
		defer mu.Unlock()
		if check() == nil {
			_ = os.Remove(lock)
		}
	}()
	if err := owned(); err != nil {
		return err
	}
	return fn(owned)
}
