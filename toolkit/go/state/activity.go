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
	Checkouts    map[string]checkoutAccess `json:"checkouts"`
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
// +link=component://docs/cleanup.md
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
		now := float64(time.Now().UnixNano()) / 1e9
		a.record.LastAccessAt = math.Max(a.record.LastAccessAt, now)
		if info != nil {
			a.record.Checkouts[Digest(info.Root)] = checkoutAccess{info.Root, info.CommonDir, math.Max(now, a.record.Checkouts[Digest(info.Root)].LastAccessAt)}
		}
		return Write(filepath.Join(directory, "activity.json"), a.record, a.owned)
	})
}
func validAccess(at float64) bool { return at >= 0 && !math.IsNaN(at) && !math.IsInf(at, 0) }
func readActivity(directory string) (activityRecord, error) {
	record := activityRecord{Version: 1, Checkouts: map[string]checkoutAccess{}}
	found, err := Read(filepath.Join(directory, "activity.json"), &record)
	if err != nil {
		return record, err
	}
	if found && (record.Version != 1 || !validAccess(record.LastAccessAt) || record.Checkouts == nil) {
		return record, errors.New("invalid repository activity record")
	}
	for key, value := range record.Checkouts {
		if !filepath.IsAbs(value.Path) || !filepath.IsAbs(value.CommonDir) || Digest(value.Path) != key || !validAccess(value.LastAccessAt) {
			return record, errors.New("invalid checkout activity identity")
		}
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
		record, err := readActivity(directory)
		if err != nil {
			return err
		}
		lease := &activityLease{directory, record, owned}
		return fn(context.WithValue(ctx, activityLeaseKey{}, lease), lease)
	})
}
