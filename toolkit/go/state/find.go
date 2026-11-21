package state

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// RepositoryMatch describes one live primary checkout. Repository is nil for a local-only clone.
type RepositoryMatch struct {
	Repository *common.Repository `json:"repository,omitempty"`
	Path       string             `json:"path"`
	Origin     string             `json:"origin,omitempty"`
	rank       int
}

type FindReport struct {
	Matches     []RepositoryMatch `json:"matches"`
	Diagnostics []string          `json:"diagnostics,omitempty"`
}

// FindRepositories searches registered local clones without refreshing their access records.
// +spec=A remote URL selects a repository identity. Each live primary checkout contributes at most one result.
// +link=component://docs/home.md
func FindRepositories(ctx context.Context, home, query string) (report FindReport, err error) {
	defer func() { err = errs.Wrap(err, "state.find_failed", "state.find", home) }()
	query = strings.TrimSpace(query)
	if query == "" {
		return report, errors.New("repository query must be nonempty")
	}
	home, err = ResolveHome(home)
	if err != nil {
		return report, err
	}
	dirs, err := directories(filepath.Join(home, "repositories"))
	if err != nil {
		return report, err
	}
	exact := project.FromOrigin(query)
	keyword := strings.ToLower(query)
	report.Matches = []RepositoryMatch{}
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		key := filepath.Base(dir)
		if exact != nil && RepositoryKey(exact.Forge.Name, exact.Path) != key {
			continue
		}
		clones, err := directories(filepath.Join(dir, "clones"))
		if err != nil {
			report.Diagnostics = append(report.Diagnostics, err.Error())
			continue
		}
		// Some commands only record clone locations. Without a persisted identity,
		// fall back to live Git just as for those existing registrations.
		var indexed *common.Repository
		if exact == nil && len(clones) > 0 {
			var id common.Repository
			found, err := Read(filepath.Join(dir, "repository.json"), &id)
			if err != nil {
				report.Diagnostics = append(report.Diagnostics, err.Error())
			}
			if found && RepositoryKey(id.Forge.Name, id.Path) == key {
				indexed = &id
			}
		}
		for _, clone := range clones {
			match, err := findClone(ctx, dir, clone, indexed, keyword)
			if err != nil {
				report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("%s: %v", clone, err))
				continue
			}
			if match == nil {
				continue
			}
			match.rank = match.searchRank(keyword)
			if exact != nil || match.rank > 0 {
				report.Matches = append(report.Matches, *match)
			}
		}
	}
	sort.Slice(report.Matches, func(i, j int) bool {
		a, b := report.Matches[i], report.Matches[j]
		if a.rank != b.rank {
			return a.rank > b.rank
		}
		return a.Path < b.Path
	})
	return report, ctx.Err()
}

func (m RepositoryMatch) searchRank(term string) int {
	name := filepath.Base(m.Path)
	if id := m.Repository; id != nil {
		name = filepath.Base(id.Path)
		if strings.EqualFold(id.Path, term) {
			return 4
		}
	}
	if strings.EqualFold(name, term) {
		return 3
	}
	if strings.Contains(strings.ToLower(name), term) {
		return 2
	}
	if strings.Contains(strings.ToLower(m.Path), term) || (m.Repository != nil && strings.Contains(strings.ToLower(m.Repository.Forge.Name+"/"+m.Repository.Path), term)) {
		return 1
	}
	return 0
}

func findClone(ctx context.Context, directory, clone string, indexed *common.Repository, keyword string) (*RepositoryMatch, error) {
	reg, found, err := readCloneRegistration(clone)
	if err != nil || !found {
		return nil, err
	}
	paths := []string{}
	if filepath.Base(reg.CommonDir) == ".git" {
		paths = append(paths, filepath.Dir(reg.CommonDir))
	}
	// Separate metadata can live anywhere. A recorded checkout supplies a candidate
	// path, and Git confirms whether its private and shared directories coincide.
	if len(paths) == 0 {
		locations := map[string]checkoutAccess{}
		if err := checkoutLocations(directory, locations); err != nil {
			return nil, err
		}
		for _, loc := range locations {
			if loc.CommonDir == reg.CommonDir {
				paths = append(paths, loc.Path)
			}
		}
		sort.Strings(paths)
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := RepositoryMatch{Repository: indexed, Path: path}
		if (indexed != nil || filepath.Base(directory) == LocalKey(reg.CommonDir)) && candidate.searchRank(keyword) == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		info, err := worktree.Local(ctx, path)
		if err != nil {
			return nil, err
		}
		if info.Linked || info.CommonDir != reg.CommonDir {
			continue
		}
		repo := git.Repository{Root: info.Root}
		origin, err := repo.Origin(ctx)
		if err != nil {
			return nil, err
		}
		id := project.FromOrigin(origin)
		key := LocalKey(reg.CommonDir)
		if id != nil {
			key = RepositoryKey(id.Forge.Name, id.Path)
		}
		if key != filepath.Base(directory) {
			continue
		}
		return &RepositoryMatch{Repository: id, Path: info.Root, Origin: displayOrigin(origin)}, nil
	}
	return nil, nil
}

// Keep clone addresses useful without printing embedded credentials or URL tokens.
func displayOrigin(origin string) string {
	if !strings.Contains(origin, "://") {
		return origin
	}
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	if u.Scheme != "ssh" {
		u.User = nil
	} else if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}
