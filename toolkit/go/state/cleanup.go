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
	"strconv"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
)

const DefaultInactiveDays = 30

type CleanOptions struct {
	Validation    func(context.Context, string, string, int, bool) (ResourceCount, error)
	cacheBefore   float64
	cacheOverflow bool
	// MergedPR verifies a recorded PR against Forge, independent of local checkout availability.
	MergedPR         func(context.Context, common.Repository, int) (bool, error)
	Home             string
	Repository       string // Empty selects all repositories already recorded in Home.
	InactiveDays     int
	DryRun           bool
	CurrentDirectory string
	// WorktreeTimeout bounds active cleanup work for each worktree. Repository cache reclamation has no local deadline.
	WorktreeTimeout time.Duration
}
type CleanAction struct {
	Repository string `json:"repository"`
	WorktreeAction
}
type CleanReport struct {
	Counts         []ResourceCount `json:"counts"`
	Home           string          `json:"home"`
	InactiveBefore time.Time       `json:"inactiveBefore"`
	DryRun         bool            `json:"dryRun"`
	Actions        []CleanAction   `json:"actions"`
	Errors         []string        `json:"errors"`
	EstimatedBytes int64           `json:"estimatedBytes"`
}

// Clean applies repository worktree retention and reclaims expired repository state. Source repositories and branch refs are never deleted.
// +spec=Dry-run shares eligibility and deletion protections with execution. Worktree cleanup uses CleanWorktrees and its repository policy. Cleanup never renews activity; missing repository activity sorts oldest. Worktree retention and errors do not prevent expired cache removal; checkout registration survives expiry, and access and writer protections still apply.
// +spec=Worktree timeouts retain partial results and continue to the next worktree. Repository cache reclamation has no local timeout; caller cancellation stops the sweep.
// +link=component://docs/home.md#清理与回收
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
	locks, err := CleanLocks(ctx, report.Home, selected, options.DryRun)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	for _, a := range locks {
		report.appendAction(ctx, selected, a)
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
	policy, e := LoadCleanupPolicy(report.Home, "")
	if e != nil {
		return report, e
	}
	candidates := []cacheCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		has, e := hasRepositoryCache(directory)
		if e != nil {
			report.Errors = append(report.Errors, e.Error())
			continue
		}
		if !has {
			continue
		}
		at, e := resourceAccess(directory)
		if e != nil {
			continue
		}
		candidates = append(candidates, cacheCandidate{path: directory, access: at})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].access == candidates[j].access {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].access < candidates[j].access
	})
	overflow := map[string]float64{}
	for i := 0; i < len(candidates)-policy.Repository.MaxCount; i++ {
		overflow[candidates[i].path] = candidates[i].access
	}
	repositoryCount := ResourceCount{Resource: "repository", Before: len(candidates), After: len(candidates), MaxCount: policy.Repository.MaxCount}
	for _, entry := range entries {
		if at, ok := overflow[filepath.Join(root, entry.Name())]; ok {
			options.cacheBefore = at
			options.cacheOverflow = true
		} else {
			options.cacheOverflow = false
		}
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
	removed := []string{}
	if options.DryRun {
		for _, a := range report.Actions {
			if a.Status == "planned" {
				removed = append(removed, a.Path)
			}
		}
	}
	for _, candidate := range candidates {
		has, e := remainingRepositoryCache(candidate.path, removed)
		if e != nil {
			report.Errors = append(report.Errors, e.Error())
		} else if !has {
			repositoryCount.After--
		}
	}
	repositoryCount.OverBudget = max(0, repositoryCount.After-repositoryCount.MaxCount)
	report.Counts = append(report.Counts, repositoryCount)
	return report, nil
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
	paths, err := filepath.Glob(filepath.Join(directory, "clones", "*", "checkouts", "*", "checkout.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		var row checkoutObservation
		found, err := Read(path, &row)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		key := filepath.Base(filepath.Dir(path))
		clone := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
		if row.Version != 1 || !filepath.IsAbs(row.Path) || !filepath.IsAbs(row.CommonDir) || Digest(row.Path) != key || Digest(row.CommonDir) != clone || row.Clone != clone {
			return errors.New("invalid checkout registration identity")
		}
		var access struct {
			Version      int     `json:"version"`
			LastAccessAt float64 `json:"lastAccessAt"`
		}
		found, err = Read(filepath.Join(filepath.Dir(path), "access.json"), &access)
		if err != nil {
			return err
		}
		if found && (access.Version != 1 || !validAccess(access.LastAccessAt)) {
			return errors.New("invalid checkout access")
		}
		checkouts[key] = checkoutAccess{Path: row.Path, CommonDir: row.CommonDir, LastAccessAt: access.LastAccessAt}
	}
	return nil
}

func (report *CleanReport) cleanRepository(ctx context.Context, directory string, options CleanOptions, protected []string) error {
	key := filepath.Base(directory)
	// Snapshot discovery under the activity lease. Remote queries and worktree
	// maintenance use their own repository lease and the caller's deadline.
	locations := map[string]checkoutAccess{}
	var discoveryErr error
	e := withActivity(ctx, directory, func(_ context.Context, _ *activityLease) error {
		discoveryErr = checkoutLocations(directory, locations)
		return nil
	})
	if e != nil {
		return e
	}
	e = discoveryErr
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
				result, e := CleanWorktrees(ctx, control, request)
				report.Counts = append(report.Counts, ResourceCount{Resource: "checkout", Repository: key, Clone: Digest(info.CommonDir), Before: result.BeforeCount, After: result.RemainingCount, MaxCount: result.MaxCount, OverBudget: result.OverCount})
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
	activity, e := readRepositoryAccess(directory)
	if e != nil {
		return e
	}
	expire := activity.LastAccessAt < float64(report.InactiveBefore.UnixNano())/1e9
	before := float64(time.Now().UnixNano()) / 1e9
	if options.cacheOverflow && activity.LastAccessAt <= options.cacheBefore {
		expire = true
		before = options.cacheBefore
	}
	if busy, err := publicationBusy(directory); err != nil {
		return err
	} else if busy {
		report.appendAction(ctx, key, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: "state_locked", Path: directory})
		return nil
	}

	merged := map[int]bool{}
	if !expire && options.MergedPR != nil {
		var repository common.Repository
		found, e := Read(filepath.Join(directory, "repository.json"), &repository)
		if e != nil {
			report.Errors = append(report.Errors, e.Error())
		} else if found && RepositoryKey(repository.Forge.Name, repository.Path) == key {
			paths, e := directories(filepath.Join(directory, "prs"))
			if e != nil {
				return e
			}
			for _, path := range paths {
				number, e := strconv.Atoi(filepath.Base(path))
				if e != nil || number < 1 {
					continue
				}
				if _, e := os.Lstat(filepath.Join(path, "comments")); errors.Is(e, os.ErrNotExist) {
					continue
				} else if e != nil {
					return e
				}
				value, e := options.MergedPR(ctx, repository, number)
				if e != nil {
					report.Errors = append(report.Errors, fmt.Sprintf("PR %d: %v", number, e))
				} else {
					merged[number] = value
				}
			}
		}
	}
	if options.Validation != nil {
		policy, err := LoadCleanupPolicy(report.Home, key)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
		} else {
			count, err := options.Validation(ctx, report.Home, key, policy.Validation.MaxCount, options.DryRun)
			count.Repository = key
			report.Counts = append(report.Counts, count)
			if err != nil {
				report.Errors = append(report.Errors, err.Error())
			}
		}
	}
	result, e := CleanRepository(ctx, CacheCleanOptions{Home: report.Home, RepositoryKey: key, DryRun: options.DryRun, Expire: expire, AccessBefore: before, Merged: merged})
	for _, action := range result.Actions {
		report.appendAction(ctx, key, action)
	}
	report.Errors = append(report.Errors, result.Errors...)
	report.Counts = append(report.Counts, result.Counts...)
	if !expire {
		report.appendAction(ctx, key, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: "recently_accessed", Path: directory, LastAccessAt: activity.LastAccessAt})
	}

	return e
}

func (report *CleanReport) appendAction(ctx context.Context, key string, a WorktreeAction) {
	report.Actions = append(report.Actions, CleanAction{key, a})
	if a.Status == "completed" || a.Status == "planned" {
		report.EstimatedBytes += a.EstimatedBytes
	}
	slog.InfoContext(ctx, "repository cleanup", "repository", key, "action", a.Action, "status", a.Status, "reason", a.Reason, "path", a.Path, "estimated_bytes", a.EstimatedBytes)
}
