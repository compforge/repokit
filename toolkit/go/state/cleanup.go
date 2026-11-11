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
// +spec=Dry-run shares eligibility and deletion protections with execution. Worktree cleanup uses CleanWorktrees and its repository policy. Cleanup never renews activity; missing repository activity sorts oldest, while failed identity checks retain resources.
// +link=component://docs/cleanup.md
func Clean(ctx context.Context, options CleanOptions) (report CleanReport, err error) {
	defer func() { err = errs.Wrap(err, "state.cleanup_failed", "state.clean", options.Home) }()
	report = CleanReport{DryRun: options.DryRun, Actions: []CleanAction{}, Errors: []string{}}
	if options.InactiveDays < 1 || options.InactiveDays > 36500 {
		return report, errors.New("inactive days must be between 1 and 36500")
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
	appendAction := func(key string, a WorktreeAction) {
		report.Actions = append(report.Actions, CleanAction{key, a})
		if a.Status == "completed" || a.Status == "planned" {
			report.EstimatedBytes += a.EstimatedBytes
		}
		slog.InfoContext(ctx, "repository cleanup", "repository", key, "action", a.Action, "status", a.Status, "reason", a.Reason, "path", a.Path, "estimated_bytes", a.EstimatedBytes)
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
			appendAction(key, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: "not_directory", Path: directory})
			continue
		}
		e := withActivity(ctx, directory, func(ctx context.Context, lease *activityLease) error {
			// Topology supplies locations, never access time. This also discovers entries
			// that have no activity record; Git must still confirm every source identity.
			if e := checkoutLocations(directory, lease.record.Checkouts); e != nil {
				return e
			}
			busy, e := stateBusy(ctx, directory, report.InactiveBefore)
			if e != nil {
				return e
			}
			retained := false
			clones := map[string]bool{}
			paths := make([]string, 0, len(lease.record.Checkouts))
			for key := range lease.record.Checkouts {
				paths = append(paths, key)
			}
			sort.Strings(paths)
			for _, checkout := range paths {
				access := lease.record.Checkouts[checkout]
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
				request := WorktreeCleanOptions{Home: report.Home, DryRun: options.DryRun, ProtectedPaths: protected}
				if options.PullRequests != nil {
					request.PullRequests, e = options.PullRequests(ctx, control)
					if e != nil {
						report.Errors = append(report.Errors, fmt.Sprintf("%s: PR status unavailable: %v", control, e))
						retained = true
					}
				}
				result, e := CleanWorktrees(ctx, control, request)
				for _, a := range result.Actions {
					appendAction(key, a)
				}
				if e != nil {
					return e
				}
				report.Errors = append(report.Errors, result.Errors...)
				retained = retained || result.RemainingCount > 0 || len(result.Errors) > 0
			}
			reason := ""
			if lease.record.LastAccessAt >= float64(report.InactiveBefore.UnixNano())/1e9 {
				reason = "recently_accessed"
			} else if retained {
				reason = "worktrees_retained"
			}
			if reason == "" {
				if busy {
					reason = "state_in_use"
				}
			}
			if reason != "" {
				appendAction(key, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: reason, Path: directory, LastAccessAt: lease.record.LastAccessAt})
				return nil
			}
			size, e := directoryBytes(ctx, directory)
			if e != nil {
				return e
			}
			if e = lease.owned(); e != nil {
				return e
			}
			status := "planned"
			if !options.DryRun {
				// Root-relative deletion does not follow symlinks out of the state tree.
				boundary, e := os.OpenRoot(root)
				if e != nil {
					return e
				}
				e = boundary.RemoveAll(key)
				closeErr := boundary.Close()
				if e = errors.Join(e, closeErr); e != nil {
					return e
				}
				status = "completed"
			}
			reason = "inactive"
			if lease.record.LastAccessAt == 0 {
				reason = "access_unknown"
			}
			appendAction(key, WorktreeAction{Action: "remove_repository_state", Status: status, Reason: reason, Path: directory, EstimatedBytes: size, LastAccessAt: lease.record.LastAccessAt})
			return nil
		})
		if e != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", key, e))
			appendAction(key, WorktreeAction{Action: "retain_repository", Status: "failed", Reason: e.Error(), Path: directory})
		}
	}
	return report, nil
}

// State writers use record leases; recent writes protect data even when they did not update activity.
func stateBusy(ctx context.Context, directory string, cutoff time.Time) (bool, error) {
	busy := false
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
				busy = true
				return filepath.SkipAll
			}
			return nil
		}
		if d.Name() == "activity.json" {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.ModTime().After(cutoff) {
			busy = true
			return filepath.SkipAll
		}
		return nil
	})
	return busy, err
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
