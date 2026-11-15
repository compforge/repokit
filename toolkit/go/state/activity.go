package state

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

type checkoutAccess struct {
	Path         string  `json:"path"`
	CommonDir    string  `json:"commonDir"`
	LastAccessAt float64 `json:"lastAccessAt"`
}
type activityRecord struct {
	Version      int                       `json:"version"`
	LastAccessAt float64                   `json:"lastAccessAt"`
	Checkouts    map[string]checkoutAccess `json:"-"`
}
type maintenanceKey struct{}
type activityLease struct {
	directory string
	record    activityRecord
	owned     func() error
}
type activityLeaseKey struct{}

func maintenanceContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, maintenanceKey{}, true)
}

// RecordAccess records use of the selected checkout, never every checkout in an inventory.
// +spec=Access is explicit RepoKit use, not filesystem atime, observation freshness or maintenance. Missing activity sorts oldest; identity and mutation protection remain mandatory.
// +link=component://docs/home.md#清理与回收
func RecordAccess(ctx context.Context, repo, home string) (err error) {
	defer func() { err = errs.Wrap(err, "state.access_failed", "state.record_access", repo) }()
	if ctx.Value(maintenanceKey{}) != nil {
		return nil
	}
	directory, info, _, err := stateLocation(ctx, repo, home)
	if err != nil {
		return err
	}
	return recordAccess(ctx, directory, &info)
}

// RecordRepositoryAccess records access to shared repository data without selecting a checkout.
func RecordRepositoryAccess(ctx context.Context, home, key string) error {
	if ctx.Value(maintenanceKey{}) != nil {
		return nil
	}
	if key == "" || filepath.Base(key) != key || key == "." || key == ".." {
		return errors.New("invalid repository key")
	}
	resolved, err := ResolveHome(home)
	if err != nil {
		return err
	}
	return recordAccess(ctx, filepath.Join(resolved, "repositories", key), nil)
}
func recordAccess(ctx context.Context, directory string, info *CheckoutInfo) error {
	return withActivity(ctx, directory, func(ctx context.Context, a *activityLease) error {
		return WithLock(ctx, filepath.Join(directory, "access"), func(owned func() error) error {
			now := float64(time.Now().UnixNano()) / 1e9
			a.record.LastAccessAt = math.Max(a.record.LastAccessAt, now)
			if info != nil {
				clone := filepath.Join(directory, "clones", Digest(info.CommonDir))
				if err := Write(filepath.Join(clone, "clone.json"), map[string]any{"version": 1, "commonDir": info.CommonDir}, owned); err != nil {
					return err
				}
				path := filepath.Join(clone, "checkouts", Digest(info.Root))
				var row checkoutObservation
				if _, err := Read(filepath.Join(path, "checkout.json"), &row); err != nil {
					return err
				}
				row.Version, row.Clone, row.CommonDir, row.Path = 1, Digest(info.CommonDir), info.CommonDir, info.Root
				if err := Write(filepath.Join(path, "checkout.json"), row, owned); err != nil {
					return err
				}
				for _, p := range []string{clone, path} {
					if err := touchAccess(p, now, owned); err != nil {
						return err
					}
				}
			}
			return Write(filepath.Join(directory, "access.json"), a.record, owned)
		})
	})
}
func validAccess(at float64) bool { return at >= 0 && !math.IsNaN(at) && !math.IsInf(at, 0) }
func readActivity(directory string) (activityRecord, error) {
	record, err := readRepositoryAccess(directory)
	if err != nil {
		return record, err
	}
	err = checkoutLocations(directory, record.Checkouts)
	return record, err
}
func readRepositoryAccess(directory string) (activityRecord, error) {
	record := activityRecord{Version: 1, Checkouts: map[string]checkoutAccess{}}
	found, err := Read(filepath.Join(directory, "access.json"), &record)
	if err != nil {
		return record, err
	}
	if found && (record.Version != 1 || !validAccess(record.LastAccessAt) || record.Checkouts == nil) {
		return record, errors.New("invalid repository activity record")
	}
	return record, nil
}
func withActivity(ctx context.Context, directory string, fn func(context.Context, *activityLease) error) error {
	if lease, ok := ctx.Value(activityLeaseKey{}).(*activityLease); ok && lease.directory == directory {
		return fn(ctx, lease)
	}
	// Outside the disposable repository tree, so deletion cannot replace a held lease.
	lock := filepath.Join(filepath.Dir(filepath.Dir(directory)), "locks", "activity", filepath.Base(directory))
	return WithLock(ctx, lock, func(owned func() error) error {
		record, err := readRepositoryAccess(directory)
		if err != nil {
			return err
		}
		lease := &activityLease{directory, record, owned}
		return fn(context.WithValue(ctx, activityLeaseKey{}, lease), lease)
	})
}

func touchAccess(directory string, now float64, owned func() error) error {
	var record struct {
		Version      int     `json:"version"`
		LastAccessAt float64 `json:"lastAccessAt"`
	}
	found, err := Read(filepath.Join(directory, "access.json"), &record)
	if err != nil {
		return err
	}
	if found && (record.Version != 1 || !validAccess(record.LastAccessAt)) {
		return errors.New("invalid resource access")
	}
	record.Version, record.LastAccessAt = 1, math.Max(now, record.LastAccessAt)
	return Write(filepath.Join(directory, "access.json"), record, owned)
}

// RecordObjectAccess renews an explicitly used object and its owning repository.
// relative identifies owner directories, never remote identities or filesystem paths outside Home.
func RecordObjectAccess(ctx context.Context, home, key string, relative ...string) error {
	if ctx.Value(maintenanceKey{}) != nil {
		return nil
	}
	for _, part := range relative {
		if part == "" || filepath.Base(part) != part || part == "." || part == ".." {
			return errors.New("invalid resource path")
		}
	}
	if err := RecordRepositoryAccess(ctx, home, key); err != nil {
		return err
	}
	resolved, err := ResolveHome(home)
	if err != nil {
		return err
	}
	directory := filepath.Join(resolved, "repositories", key)
	return withActivity(ctx, directory, func(_ context.Context, lease *activityLease) error {
		now := float64(time.Now().UnixNano()) / 1e9
		if len(relative) > 2 && (relative[0] == "prs" || relative[0] == "clones") {
			if err := touchAccess(filepath.Join(directory, relative[0], relative[1]), now, lease.owned); err != nil {
				return err
			}
		}
		return touchAccess(filepath.Join(append([]string{directory}, relative...)...), now, lease.owned)
	})
}
