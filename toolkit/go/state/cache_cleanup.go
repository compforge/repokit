package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// CacheCleanOptions selects Home-owned records, never remote objects or Git refs.
type CacheCleanOptions struct {
	Home          string
	RepositoryKey string
	CloneKey      string
	PRNumber      int
	Policy        *CleanupPolicy
	DryRun        bool
	// Expire reclaims eligible caches irrespective of count, retaining registration, tracking intent and evidence.
	Expire bool
	// AccessBefore fences a candidate selected before foreground work resumed. Zero uses invocation time.
	AccessBefore float64
	Merged       map[int]bool
}
type ResourceCount struct {
	Repository string `json:"repository,omitempty"`
	Clone      string `json:"clone,omitempty"`
	Resource   string `json:"resource"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
	MaxCount   int    `json:"max_count"`
	OverBudget int    `json:"over_budget"`
}
type CacheCleanReport struct {
	Actions  []WorktreeAction `json:"actions"`
	Counts   []ResourceCount  `json:"counts"`
	Errors   []string         `json:"errors"`
	Affected []string         `json:"affected"`
	DryRun   bool             `json:"dry_run"`
}
type cacheCandidate struct {
	path   string
	count  int
	access float64
	reason string
	number int
}
type cacheCleanup struct {
	ctx       context.Context
	directory string
	options   CacheCleanOptions
	policy    CleanupPolicy
	owned     func() error
	report    CacheCleanReport
}

// CleanRepository composes domain-owned cache cleanup. Validation and physical resources keep their own retention.
// +spec=Ownership permits composition; a reference only triggers re-evaluation. Configuration, registration and creation provenance survive cache eviction. Dry-run uses identical candidates and no record mutations.
// +link=component://docs/home.md#关联清理与全局调度
func CleanRepository(ctx context.Context, options CacheCleanOptions) (CacheCleanReport, error) {
	return cleanCaches(ctx, options, "repository")
}
func CleanPullRequests(ctx context.Context, options CacheCleanOptions) (CacheCleanReport, error) {
	return cleanCaches(ctx, options, "pr")
}
func CleanComments(ctx context.Context, options CacheCleanOptions) (CacheCleanReport, error) {
	return cleanCaches(ctx, options, "comment")
}
func CleanBranches(ctx context.Context, options CacheCleanOptions) (CacheCleanReport, error) {
	return cleanCaches(ctx, options, "branch")
}
func CleanClones(ctx context.Context, options CacheCleanOptions) (CacheCleanReport, error) {
	return cleanCaches(ctx, options, "clone")
}
func cleanCaches(ctx context.Context, options CacheCleanOptions, domain string) (report CacheCleanReport, err error) {
	defer func() { err = errs.Wrap(err, "state.cleanup_failed", "state.clean_"+domain, options.RepositoryKey) }()
	c := cacheCleanup{ctx: maintenanceContext(ctx), options: options, report: CacheCleanReport{Actions: []WorktreeAction{}, Counts: []ResourceCount{}, Errors: []string{}, Affected: []string{}, DryRun: options.DryRun}}
	if options.RepositoryKey == "" || filepath.Base(options.RepositoryKey) != options.RepositoryKey || options.RepositoryKey == "." || options.RepositoryKey == ".." {
		return c.report, errors.New("repository key is required")
	}
	if options.CloneKey != "" && (filepath.Base(options.CloneKey) != options.CloneKey || options.CloneKey == "." || options.CloneKey == "..") {
		return c.report, errors.New("invalid clone key")
	}
	if options.PRNumber < 0 {
		return c.report, errors.New("invalid PR number")
	}
	home, err := ResolveHome(options.Home)
	if err != nil {
		return c.report, err
	}
	c.directory = filepath.Join(home, "repositories", options.RepositoryKey)
	if info, e := os.Lstat(filepath.Dir(c.directory)); e == nil && !info.IsDir() {
		return c.report, errors.New("repositories must be a real directory")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return c.report, e
	}
	info, err := os.Lstat(c.directory)
	if errors.Is(err, os.ErrNotExist) {
		return c.report, nil
	}
	if err != nil {
		return c.report, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return c.report, errors.New("repository cache must be a real directory")
	}
	if busy, e := publicationBusy(c.directory); e != nil {
		return c.report, e
	} else if busy {
		c.report.Actions = append(c.report.Actions, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: "state_locked", Path: c.directory})
		return c.report, nil
	}

	c.policy, err = LoadCleanupPolicy(home, options.RepositoryKey)
	if err != nil {
		return c.report, err
	}
	if options.Policy != nil {
		c.policy = *options.Policy
	}
	if err = c.policy.Validate(); err != nil {
		return c.report, err
	}
	if c.options.AccessBefore == 0 {
		c.options.AccessBefore = float64(time.Now().UnixNano()) / 1e9
	}
	err = withActivity(c.ctx, c.directory, func(ctx context.Context, lease *activityLease) error {
		// Access and publication are separate leases; all cleanup acquires them in this order.
		return WithLock(ctx, filepath.Join(c.directory, "cleanup"), func(owned func() error) error {
			c.owned = func() error { return errors.Join(lease.owned(), owned()) }
			if domain == "repository" && lease.record.LastAccessAt > c.options.AccessBefore {
				c.report.Actions = append(c.report.Actions, WorktreeAction{Action: "retain_repository", Status: "skipped", Reason: "access_changed", Path: c.directory})
				return nil
			}
			tasks := []struct {
				name string
				run  func() error
			}{{"pr", c.prs}, {"comment", c.comments}, {"branch", c.branches}, {"clone", c.clones}}
			for _, task := range tasks {
				if domain != task.name && domain != "repository" {
					continue
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := task.run(); err != nil {
					c.report.Errors = append(c.report.Errors, task.name+": "+err.Error())
				}
			}
			if domain == "repository" && c.options.Expire {
				for _, name := range []string{"metadata.json", "remotes"} {
					if err := c.remove(cacheCandidate{path: filepath.Join(c.directory, name)}, "repository_cache", "inactive"); err != nil {
						c.report.Errors = append(c.report.Errors, err.Error())
					}
				}
			}
			return nil
		})
	})
	if err == nil {
		err = c.ctx.Err()
	}
	for i := range c.report.Counts {
		c.report.Counts[i].Repository = options.RepositoryKey
	}
	return c.report, err
}
func directories(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("resource collection must be a real directory")
	}

	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			out = append(out, filepath.Join(path, entry.Name()))
		}
	}
	return out, nil
}
func resourceAccess(path string) (float64, error) {
	var r struct {
		Version      int     `json:"version"`
		LastAccessAt float64 `json:"lastAccessAt"`
	}
	found, err := Read(filepath.Join(path, "access.json"), &r)
	if err != nil {
		return 0, err
	}
	if found && (r.Version != 1 || !validAccess(r.LastAccessAt)) {
		return 0, errors.New("invalid resource access")
	}
	return r.LastAccessAt, nil
}
func (c *cacheCleanup) collect(paths []string) ([]cacheCandidate, error) {
	rows := []cacheCandidate{}
	for _, path := range paths {
		at, err := resourceAccess(path)
		if err != nil {
			return nil, err
		}
		rows = append(rows, cacheCandidate{path: path, count: 1, access: at})
	}
	return rows, nil
}
func (c *cacheCleanup) apply(rows []cacheCandidate, resource string, maxCount int) error {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].access == rows[j].access {
			return rows[i].path < rows[j].path
		}
		return rows[i].access < rows[j].access
	})
	count := ResourceCount{Resource: resource, MaxCount: maxCount}
	for _, row := range rows {
		count.Before += row.count
	}
	count.After = count.Before
	// Keep a fixed newest window. Protected older entries never displace it.
	cutoff := len(rows)
	retained := 0
	for cutoff > 0 && retained < maxCount {
		if retained > 0 && retained+rows[cutoff-1].count > maxCount {
			break
		}
		cutoff--
		retained += rows[cutoff].count
	}
	for index, row := range rows {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		reason := "max_count"
		if c.options.Expire {
			reason = "inactive"
		}
		if c.options.Merged[row.number] && resource == "comment" {
			reason = "pr_merged"
		}
		if !c.options.Expire && index >= cutoff && reason != "pr_merged" {
			continue
		}
		if row.access > c.options.AccessBefore {
			row.reason = "access_changed"
		}
		if row.reason != "" {
			c.report.Actions = append(c.report.Actions, WorktreeAction{Action: "retain_" + resource, Status: "skipped", Reason: row.reason, Path: row.path})
			continue
		}
		if err := c.remove(row, resource, reason); err != nil {
			c.report.Errors = append(c.report.Errors, err.Error())
			continue
		}
		count.After -= row.count
	}
	count.OverBudget = max(0, count.After-maxCount)
	c.report.Counts = append(c.report.Counts, count)
	return nil
}
func (c *cacheCleanup) remove(row cacheCandidate, resource, reason string) error {
	for _, action := range c.report.Actions {
		if (action.Status == "planned" || action.Status == "completed") && (row.path == action.Path || withinDirectory(action.Path, row.path)) {
			return nil
		}
	}

	info, err := os.Lstat(row.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink cache %s", row.path)
	}
	size, err := directoryBytes(c.ctx, row.path)
	if err != nil {
		return err
	}
	action := WorktreeAction{Action: "remove_" + resource, Status: "planned", Reason: reason, Path: row.path, PRNumber: row.number, EstimatedBytes: size, LastAccessAt: row.access}
	if !c.options.DryRun {
		if err := c.owned(); err != nil {
			return err
		}
		relative, err := filepath.Rel(c.directory, row.path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("invalid cleanup boundary")
		}
		root, err := os.OpenRoot(c.directory)
		if err != nil {
			return err
		}
		err = errors.Join(root.RemoveAll(relative), root.Close())
		if err != nil {
			return err
		}
		action.Status = "completed"
	}
	slog.InfoContext(c.ctx, "cache cleanup", "resource", resource, "path", row.path, "reason", reason, "status", action.Status)
	c.report.Actions = append(c.report.Actions, action)
	c.report.Affected = append(c.report.Affected, row.path)
	return nil
}
func (c *cacheCleanup) prPaths() ([]string, error) {
	paths, err := directories(filepath.Join(c.directory, "prs"))
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, path := range paths {
		n, err := strconv.Atoi(filepath.Base(path))
		if err == nil && n > 0 && strconv.Itoa(n) == filepath.Base(path) && (c.options.PRNumber == 0 || c.options.PRNumber == n) {
			result = append(result, path)
		}
	}
	return result, nil
}
func trackedPR(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "access.json" || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var r struct {
			Version     int      `json:"version"`
			ScopeIDs    []string `json:"scopeIds"`
			Observation struct {
				NextRefreshAt *string `json:"nextRefreshAt"`
			} `json:"observation"`
		}
		found, err := Read(filepath.Join(path, entry.Name()), &r)
		if err != nil {
			return false, err
		}
		if found && r.Version != 1 {
			return false, errors.New("invalid PR record")
		}
		if len(r.ScopeIDs) > 0 || r.Observation.NextRefreshAt != nil {
			return true, nil
		}
	}
	return false, nil
}
func (c *cacheCleanup) prs() error {
	paths, err := c.prPaths()
	if err != nil {
		return err
	}
	rows, err := c.collect(paths)
	if err != nil {
		return err
	}
	for i := range rows {
		tracked, err := trackedPR(rows[i].path)
		if err != nil {
			return err
		}
		if tracked {
			rows[i].reason = "observation_tracked"
		}
		rows[i].number, _ = strconv.Atoi(filepath.Base(rows[i].path))
	}
	// Removing a PR owns its whole comment collection. The comments pass excludes planned parent removals.
	before := len(c.report.Actions)
	if err := c.apply(rows, "pr", c.policy.PR.MaxCount); err != nil {
		return err
	}
	removed := false
	for _, a := range c.report.Actions[before:] {
		removed = removed || a.Action == "remove_pr" && (a.Status == "completed" || a.Status == "planned")
	}
	if removed {
		return c.invalidateAssociations()
	}
	return nil
}
func (c *cacheCleanup) comments() error {
	paths, err := c.prPaths()
	if err != nil {
		return err
	}
	rows := []cacheCandidate{}
	for _, path := range paths {
		covered := false
		for _, a := range c.report.Actions {
			if a.Path == path && a.Action == "remove_pr" && (a.Status == "planned" || a.Status == "completed") {
				covered = true
			}
		}
		if covered {
			continue
		}
		root := filepath.Join(path, "comments")
		comments, err := directories(root)
		if err != nil {
			return err
		}
		count := 0
		for _, comment := range comments {
			entries, e := os.ReadDir(comment)
			if e != nil {
				return e
			}
			for _, entry := range entries {
				if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
					count++
					break
				}
			}
		}
		if count == 0 {
			continue
		}
		at, err := resourceAccess(root)
		if err != nil {
			return err
		}
		number, _ := strconv.Atoi(filepath.Base(path))
		rows = append(rows, cacheCandidate{path: root, count: count, access: at, number: number})
	}
	return c.apply(rows, "comment", c.policy.Comment.MaxCount)
}
func (c *cacheCleanup) branchPaths() ([]string, error) {
	clones, err := directories(filepath.Join(c.directory, "clones"))
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, clone := range clones {
		if c.options.CloneKey != "" && filepath.Base(clone) != c.options.CloneKey {
			continue
		}
		branches, err := directories(filepath.Join(clone, "branches"))
		if err != nil {
			return nil, err
		}
		paths = append(paths, branches...)
	}
	return paths, nil
}
func (c *cacheCleanup) branches() error {
	paths, err := c.branchPaths()
	if err != nil {
		return err
	}
	rows := []cacheCandidate{}
	for _, path := range paths {
		has := false
		for _, name := range []string{"branch.json", "associations"} {
			if _, err := os.Lstat(filepath.Join(path, name)); err == nil {
				has = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if !has {
			continue
		}
		at, err := resourceAccess(path)
		if err != nil {
			return err
		}
		rows = append(rows, cacheCandidate{path: path, count: 1, access: at})
	}
	// Provenance belongs to the branch owner but outlives its cache.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].access == rows[j].access {
			return rows[i].path < rows[j].path
		}
		return rows[i].access < rows[j].access
	})
	count := ResourceCount{Resource: "branch", Before: len(rows), After: len(rows), MaxCount: c.policy.Branch.MaxCount}
	for index, row := range rows {
		if !c.options.Expire && index >= max(0, len(rows)-count.MaxCount) {
			break
		}
		if row.access > c.options.AccessBefore {
			c.report.Actions = append(c.report.Actions, WorktreeAction{Action: "retain_branch", Status: "skipped", Reason: "access_changed", Path: row.path})
			continue
		}
		ok := true
		for _, name := range []string{"branch.json", "associations"} {
			part := row
			part.path = filepath.Join(row.path, name)
			if err := c.remove(part, "branch_cache", "max_count"); err != nil {
				c.report.Errors = append(c.report.Errors, err.Error())
				ok = false
			}
		}
		if ok {
			count.After--
		}
	}
	count.OverBudget = max(0, count.After-count.MaxCount)
	c.report.Counts = append(c.report.Counts, count)
	return nil
}
func (c *cacheCleanup) invalidateAssociations() error {
	paths, err := c.branchPaths()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := c.remove(cacheCandidate{path: filepath.Join(path, "associations")}, "branch_associations", "pr_evicted"); err != nil {
			return err
		}
	}
	return nil
}
func (c *cacheCleanup) clones() error {
	paths, err := directories(filepath.Join(c.directory, "clones"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if c.options.CloneKey != "" && filepath.Base(path) != c.options.CloneKey {
			continue
		}
		var clone struct {
			Version   int    `json:"version"`
			CommonDir string `json:"commonDir"`
		}
		found, err := Read(filepath.Join(path, "clone.json"), &clone)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if clone.Version != 1 || !filepath.IsAbs(clone.CommonDir) || Digest(clone.CommonDir) != filepath.Base(path) {
			return errors.New("invalid clone registration")
		}
		checkouts, err := directories(filepath.Join(path, "checkouts"))
		if err != nil {
			return err
		}
		for _, checkout := range checkouts {
			var row checkoutObservation
			found, err := Read(filepath.Join(checkout, "checkout.json"), &row)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if row.CommonDir != clone.CommonDir || row.Clone != filepath.Base(path) || !filepath.IsAbs(row.Path) || Digest(row.Path) != filepath.Base(checkout) {
				return errors.New("invalid checkout registration")
			}
			if _, err = os.Lstat(row.Path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err = c.remove(cacheCandidate{path: checkout}, "checkout_registration", "path_missing"); err != nil {
				return err
			}
		}
		if _, err = os.Lstat(clone.CommonDir); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// The clone directory may still own creation provenance. Retire only its missing location.
		if err := c.remove(cacheCandidate{path: filepath.Join(path, "clone.json")}, "clone_registration", "path_missing"); err != nil {
			return err
		}
	}
	return nil
}
