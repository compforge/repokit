package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

const DefaultInactiveDays = 30

type CleanOptions struct {
	Home             string
	Repository       string // Empty selects all repositories already recorded in Home.
	InactiveDays     int
	DryRun           bool
	CurrentDirectory string
	// WorktreeTimeout bounds active cleanup work for each worktree. Repository cache reclamation has no local deadline.
	WorktreeTimeout time.Duration
	// PullRequests resolves the same live PR capability used by direct worktree cleanup.
	PullRequests func(context.Context, string) (WorktreePullRequests, error)
}
type CleanAction struct {
	Repository string `json:"repository"`
	WorktreeAction
}
type CleanReport struct {
	Home           string        `json:"home"`
	InactiveBefore time.Time     `json:"inactiveBefore"`
	DryRun         bool          `json:"dryRun"`
	Actions        []CleanAction `json:"actions"`
	Errors         []string      `json:"errors"`
	EstimatedBytes int64         `json:"estimatedBytes"`
}

// Clean applies repository worktree retention and reclaims expired repository state. Source repositories and branch refs are never deleted.
// +spec=Dry-run shares eligibility and deletion protections with execution. Worktree cleanup uses CleanWorktrees and its repository policy. Cleanup never renews activity; missing repository activity sorts oldest. Worktree retention and errors do not prevent expired cache removal; checkout registration survives expiry, and access and writer protections still apply.
// +spec=Worktree timeouts retain partial results and continue to the next worktree. Repository cache reclamation has no local timeout; caller cancellation stops the sweep.
// +link=component://docs/cleanup.md
func Clean(ctx context.Context, options CleanOptions) (report CleanReport, err error) {
	defer func() { err = errs.Wrap(err, "state.cleanup_failed", "state.clean", options.Home) }()
	report = CleanReport{DryRun: options.DryRun, Actions: []CleanAction{}, Errors: []string{}}
	if options.InactiveDays < 1 || options.InactiveDays > 36500 {
		return report, errors.New("inactive days must be between 1 and 36500")
	}
	if options.WorktreeTimeout < 0 {
		return report, errors.New("worktree timeout must not be negative")
	}
	report.Home, err = ResolveHome(options.Home)
	if err != nil {
		return report, err
	}
	if options.CurrentDirectory == "" {
		options.CurrentDirectory, err = os.Getwd()
		if err != nil {
			return report, err
		}
	}
	options.CurrentDirectory, err = filepath.EvalSymlinks(options.CurrentDirectory)
	if err != nil {
		return report, err
	}
	report.InactiveBefore = time.Now().UTC().Add(-time.Duration(options.InactiveDays) * 24 * time.Hour)
	ctx = maintenanceContext(ctx)
	var selected string
	protected := []string{options.CurrentDirectory}
	if options.Repository != "" {
		directory, checkout, _, e := stateLocation(ctx, options.Repository, report.Home)
		if e != nil {
			return report, e
		}
		selected = filepath.Base(directory)
		protected = append(protected, checkout.Root)
	}
	root := filepath.Join(report.Home, "repositories")
	info, e := os.Lstat(root)
	if errors.Is(e, os.ErrNotExist) {
		return report, nil
	}
	if e != nil {
		return report, e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return report, errors.New("repositories must be a real directory")
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return report, e
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		key := entry.Name()
		if selected != "" && key != selected {
			continue
		}
		directory := filepath.Join(root, key)
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			report.appendAction(ctx, key, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: "not_directory", Path: directory})
			continue
		}
		started := time.Now()
		previousErrors := len(report.Errors)
		slog.InfoContext(ctx, "repository cleanup started", "repository", key, "worktree_timeout", options.WorktreeTimeout)
		e := report.cleanRepository(ctx, directory, options, protected)
		if e != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", key, e))
			report.appendAction(ctx, key, WorktreeAction{Action: "retain_repository", Status: "failed", Reason: e.Error(), Path: directory})
		}
		slog.InfoContext(ctx, "repository cleanup finished", "repository", key, "elapsed", time.Since(started), "errors", len(report.Errors)-previousErrors, "error", e)
		if err := ctx.Err(); err != nil {
			return report, err
		}
	}
	return report, nil
}

// Active record writers are protected; disposable cache mtime is not access.
func stateProtection(ctx context.Context, directory string) (string, error) {
	reason := ""
	err := filepath.WalkDir(directory, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if strings.HasSuffix(d.Name(), ".lock") {
				reason = "state_locked"
				return filepath.SkipAll
			}
			return nil
		}
		return nil
	})
	return reason, err
}
func directoryBytes(ctx context.Context, path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type().IsRegular() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func checkoutLocations(directory string, checkouts map[string]checkoutAccess) error {
	entries, err := os.ReadDir(filepath.Join(directory, "checkouts"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		var row checkoutObservation
		found, err := Read(filepath.Join(directory, "checkouts", entry.Name(), "observation.json"), &row)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if row.Version != 1 || !filepath.IsAbs(row.Path) || !filepath.IsAbs(row.CommonDir) || Digest(row.Path) != entry.Name() || Digest(row.CommonDir) != row.Clone {
			return errors.New("invalid checkout observation identity")
		}
		if current, ok := checkouts[entry.Name()]; ok {
			if current.Path != row.Path || current.CommonDir != row.CommonDir {
				return errors.New("checkout activity and topology disagree")
			}
		} else {
			checkouts[entry.Name()] = checkoutAccess{Path: row.Path, CommonDir: row.CommonDir}
		}
	}
	return nil
}

func (report *CleanReport) cleanRepository(ctx context.Context, directory string, options CleanOptions, protected []string) error {
	key := filepath.Base(directory)
	// Snapshot discovery under the activity lease. Remote queries and worktree
	// maintenance use their own repository lease and the caller's deadline.
	locations := map[string]checkoutAccess{}
	e := withActivity(ctx, directory, func(_ context.Context, lease *activityLease) error {
		for key, access := range lease.record.Checkouts {
			locations[key] = access
		}
		return nil
	})
	if e != nil {
		return e
	}
	e = checkoutLocations(directory, locations)
	if e == nil {
		e = func() error {
			clones := map[string]bool{}
			paths := make([]string, 0, len(locations))
			for key := range locations {
				paths = append(paths, key)
			}
			sort.Strings(paths)
			for _, checkout := range paths {
				if err := ctx.Err(); err != nil {
					return err
				}
				access := locations[checkout]
				if clones[access.CommonDir] {
					continue
				}
				_, e := os.Lstat(access.Path)
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				if e != nil {
					return e
				}
				live, info, _, e := stateLocation(ctx, access.Path, report.Home)
				if e != nil {
					return e
				}
				if live != directory || info.CommonDir != access.CommonDir {
					return fmt.Errorf("checkout identity changed: %s", access.Path)
				}
				topology, e := Checkout(ctx, access.Path)
				if e != nil {
					return e
				}
				control := access.Path
				if topology.MainRoot != "" {
					control = topology.MainRoot
				}
				clones[access.CommonDir] = true
				request := WorktreeCleanOptions{Timeout: options.WorktreeTimeout, Home: report.Home, DryRun: options.DryRun, ProtectedPaths: protected}
				if options.PullRequests != nil {
					request.PullRequests, e = options.PullRequests(ctx, control)
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if e != nil {
						report.Errors = append(report.Errors, fmt.Sprintf("%s: PR status unavailable: %v", control, e))
					}
				}
				result, e := CleanWorktrees(ctx, control, request)
				for _, a := range result.Actions {
					report.appendAction(ctx, key, a)
				}
				report.Errors = append(report.Errors, result.Errors...)
				if e != nil {
					return e
				}
			}
			return nil
		}()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: worktree discovery or cleanup: %v", key, e))
	}
	// Re-read access under a fresh lease: a foreground access during Git/PR
	// maintenance must retain the cache. Worktree outcomes do not veto expiry.
	e = withActivity(ctx, directory, func(ctx context.Context, lease *activityLease) error {
		reason := ""
		if lease.record.LastAccessAt >= float64(report.InactiveBefore.UnixNano())/1e9 {
			reason = "recently_accessed"
		}
		if reason == "" {
			// Protect active record writers, including those that started during
			// remote maintenance. Cache mtime does not renew user access.
			var e error
			reason, e = stateProtection(ctx, directory)
			if e != nil {
				return e
			}
		}
		if reason != "" {
			report.appendAction(ctx, key, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: reason, Path: directory, LastAccessAt: lease.record.LastAccessAt})
			return nil
		}
		action, e := reclaimRepositoryState(ctx, lease, options.DryRun)
		if e != nil {
			return e
		}
		report.appendAction(ctx, key, action)
		return nil
	})
	return e
}

func (report *CleanReport) appendAction(ctx context.Context, key string, a WorktreeAction) {
	report.Actions = append(report.Actions, CleanAction{key, a})
	if a.Status == "completed" || a.Status == "planned" {
		report.EstimatedBytes += a.EstimatedBytes
	}
	slog.InfoContext(ctx, "repository cleanup", "repository", key, "action", a.Action, "status", a.Status, "reason", a.Reason, "path", a.Path, "estimated_bytes", a.EstimatedBytes)
}
