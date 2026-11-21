package state

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

type cloneRegistration struct {
	Version   int    `json:"version"`
	CommonDir string `json:"commonDir"`
}

func readCloneRegistration(path string) (cloneRegistration, bool, error) {
	var clone cloneRegistration
	found, err := Read(filepath.Join(path, "clone.json"), &clone)
	if found && (clone.Version != 1 || !filepath.IsAbs(clone.CommonDir) || Digest(clone.CommonDir) != filepath.Base(path)) {
		return clone, false, errors.New("invalid clone registration")
	}
	return clone, found, err
}

type checkoutObservation struct {
	Version    int     `json:"version"`
	Clone      string  `json:"clone"`
	CommonDir  string  `json:"commonDir"`
	Path       string  `json:"path"`
	Head       string  `json:"head"`
	Branch     string  `json:"branch"`
	Primary    bool    `json:"primary"`
	ObservedAt float64 `json:"observedAt"`
}
type cloneBranch struct {
	Complete   bool     `json:"complete"`
	Head       string   `json:"head"`
	CommonDir  string   `json:"commonDir"`
	Checkouts  []string `json:"checkouts"`
	ObservedAt float64  `json:"observedAt"`
}
type branchRecord struct {
	Version int    `json:"version"`
	Branch  string `json:"branch"`
	cloneBranch
}

// BranchObservation contains one clone's observed branch, not a cross-clone tip.
type BranchObservation struct {
	Complete   bool            `json:"complete"`
	Branch     string          `json:"branch"`
	Head       string          `json:"head"`
	Checkouts  []CheckoutEntry `json:"checkouts"`
	ObservedAt float64         `json:"observedAt"`
	Source     string          `json:"source"`
}

func stateLocation(ctx context.Context, repo, home string) (string, CheckoutInfo, any, error) {
	resolved, err := ResolveHome(home)
	if err != nil {
		return "", CheckoutInfo{}, nil, err
	}
	home = resolved
	info, err := worktree.Local(ctx, repo)
	if err != nil {
		return "", info, nil, err
	}
	repository, err := git.Open(ctx, repo)
	if err != nil {
		return "", info, nil, err
	}
	origin, err := repository.Origin(ctx)
	if err != nil {
		return "", info, nil, err
	}
	identity := project.FromOrigin(origin)
	key := LocalKey(info.CommonDir)
	var record any = map[string]string{"local": info.CommonDir}
	if identity != nil {
		key = RepositoryKey(identity.Forge.Name, identity.Path)
		record = identity
	}
	return filepath.Join(home, "repositories", key), info, record, nil
}

var errIncompleteObservation = errors.New("incomplete checkout observation")

// LoadBranch uses a local observation only within maxAge; zero requests current Git facts.
// +spec=`Missing, stale or partially published observations are refreshed for the requested branch only. Failed current reads never return stale data as current.`
// +link=component://docs/home.md
func LoadBranch(ctx context.Context, repo, branch, home string, maxAge time.Duration) (observation *BranchObservation, err error) {
	defer func() { err = errs.Wrap(err, "state.branch_failed", "state.load_branch", repo) }()
	if maxAge < 0 {
		return nil, errors.New("max age must be nonnegative")
	}
	if err := RecordAccess(ctx, repo, home); err != nil {
		return nil, err
	}
	directory, info, _, err := stateLocation(ctx, repo, home)
	if err != nil {
		return nil, err
	}
	clone := Digest(info.CommonDir)
	if err := RecordObjectAccess(ctx, home, filepath.Base(directory), "clones", clone, "branches", Part(branch)); err != nil {
		return nil, err
	}
	var result *BranchObservation
	read := func(source string) error {
		return WithLock(ctx, RepositoryLock(directory, "topology"), func(owned func() error) error {
			var record branchRecord
			found, err := Read(filepath.Join(directory, "clones", clone, "branches", Part(branch), "branch.json"), &record)
			if err != nil || !found {
				return err
			}
			if record.Version != 1 || record.Branch != branch || record.CommonDir != info.CommonDir {
				return errors.New("invalid branch observation identity")
			}
			observed := record.cloneBranch
			if observed.Head == "" {
				return nil
			}
			result = &BranchObservation{Complete: observed.Complete, Branch: branch, Head: observed.Head, ObservedAt: observed.ObservedAt, Source: source, Checkouts: []CheckoutEntry{}}
			for _, key := range observed.Checkouts {
				if _, err := hex.DecodeString(key); err != nil || len(key) != 32 {
					return errors.New("invalid checkout observation key")
				}
				var row checkoutObservation
				found, err := Read(filepath.Join(directory, "clones", clone, "checkouts", key, "checkout.json"), &row)
				if err != nil {
					return err
				}
				if !found || row.Version != 1 || row.Clone != clone || row.Branch != branch || row.ObservedAt != observed.ObservedAt {
					result = nil
					return errIncompleteObservation
				}
				result.Checkouts = append(result.Checkouts, CheckoutEntry{Path: row.Path, SHA: row.Head, Branch: branch, Primary: row.Primary})
			}
			return owned()
		})
	}
	if maxAge > 0 {
		if err := read("cache"); err != nil {
			if !errors.Is(err, errIncompleteObservation) {
				return nil, err
			}
			slog.InfoContext(ctx, "repairing incomplete branch observation", "repository", filepath.Base(directory), "branch", branch)
		}
	}
	if result != nil {
		age := time.Duration((float64(time.Now().UnixNano())/1e9 - result.ObservedAt) * 1e9)
		if age >= 0 && age < maxAge {
			return result, nil
		}
	}
	if _, err := observeBranch(ctx, repo, home, false, &branch); err != nil {
		return nil, err
	}
	result = nil
	if err := read("git"); err != nil {
		return nil, err
	}
	return result, nil
}

// Observe persists local branch/checkout facts without claiming complete Forge inventory.
// Reconcile retires only this clone's absent checkout records and empty branch projections.
// +spec=`Failed inventory never means absence. Observations are not authority to delete Git resources. History survives checkout reclamation.`
// +link=component://docs/home.md
func Observe(ctx context.Context, repo, home string, reconcile bool) (directory string, err error) {
	defer func() { err = errs.Wrap(err, "state.observe_failed", "state.observe", repo) }()
	if err := RecordAccess(ctx, repo, home); err != nil {
		return "", err
	}
	return observeBranch(ctx, repo, home, reconcile, nil)
}

func observeBranch(ctx context.Context, repo, home string, reconcile bool, selectedBranch *string) (string, error) {
	directory, info, identity, err := stateLocation(ctx, repo, home)
	if err != nil {
		return "", err
	}
	clone := Digest(info.CommonDir)
	err = WithLock(ctx, RepositoryLock(directory, "topology"), func(owned func() error) error {
		out, err := worktreeQuery(ctx, repo, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/")
		if err != nil {
			return err
		}
		branches := map[string]string{}
		for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if line == "" {
				continue
			}
			name, head, ok := strings.Cut(line, "\x00")
			if !ok || !strings.HasPrefix(name, "refs/heads/") || head == "" {
				return errors.New("invalid branch inventory")
			}
			branches[strings.TrimPrefix(name, "refs/heads/")] = head
		}
		entries, err := ListCheckouts(ctx, repo)
		if err != nil {
			return err
		}
		current, _, _, err := stateLocation(ctx, repo, home)
		if err != nil {
			return err
		}
		if current != directory {
			return errors.New("repository identity changed during observation")
		}
		at := float64(time.Now().UnixNano()) / 1e9
		rows := map[string]checkoutObservation{}
		complete := true
		for _, e := range entries {
			if selectedBranch != nil && e.Branch != *selectedBranch {
				continue
			}
			if e.Path == "" {
				complete = false
				continue
			}
			path, err := filepath.EvalSymlinks(e.Path)
			if errors.Is(err, os.ErrNotExist) {
				path = e.Path
			} else if err != nil {
				return err
			}
			rows[Digest(path)] = checkoutObservation{1, clone, info.CommonDir, path, e.SHA, e.Branch, e.Primary, at}
		}
		previous := map[string]checkoutObservation{}
		paths, err := filepath.Glob(filepath.Join(directory, "clones", clone, "checkouts", "*", "checkout.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			if selectedBranch != nil {
				break
			}
			var row checkoutObservation
			found, err := Read(path, &row)
			if err != nil {
				return err
			}
			if found && row.Clone == clone {
				previous[filepath.Base(filepath.Dir(path))] = row
			}
		}
		if err := Write(filepath.Join(directory, "repository.json"), identity, owned); err != nil {
			return err
		}
		if err := Write(filepath.Join(directory, "clones", clone, "clone.json"), map[string]any{"version": 1, "commonDir": info.CommonDir}, owned); err != nil {
			return err
		}
		for key, row := range rows {
			root := filepath.Join(directory, "clones", clone, "checkouts", key)
			if err := Write(filepath.Join(root, "checkout.json"), row, owned); err != nil {
				return err
			}
		}
		names := map[string]bool{}
		if selectedBranch != nil {
			names[*selectedBranch] = true
		} else {
			for name := range branches {
				names[name] = true
			}
		}
		paths, err = filepath.Glob(filepath.Join(directory, "clones", clone, "branches", "*", "branch.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			if selectedBranch != nil {
				break
			}
			var record branchRecord
			found, err := Read(path, &record)
			if err != nil {
				return err
			}
			if found {
				if record.CommonDir == info.CommonDir {
					names[record.Branch] = true
				}
			}
		}
		for branch := range names {
			root := filepath.Join(directory, "clones", clone, "branches", Part(branch))
			path := filepath.Join(root, "branch.json")
			record := branchRecord{Version: 1, Branch: branch}
			if _, err := Read(path, &record); err != nil {
				return err
			}
			if record.Version != 1 || record.Branch != branch {
				return errors.New("branch observation identity mismatch")
			}
			links := []string{}
			for key, row := range rows {
				if row.Branch == branch {
					links = append(links, key)
				}
			}
			sort.Strings(links)
			if head, ok := branches[branch]; ok {
				branchComplete := true
				for _, entry := range entries {
					if entry.Branch == branch && entry.Path == "" {
						branchComplete = false
					}
				}
				record.cloneBranch = cloneBranch{Complete: branchComplete, Head: head, CommonDir: info.CommonDir, Checkouts: links, ObservedAt: at}
			} else {
				record.cloneBranch = cloneBranch{CommonDir: info.CommonDir, ObservedAt: at}
			}
			if err := Write(path, record, owned); err != nil {
				return err
			}

			if reconcile && record.Head == "" {
				if err := retireBranch(root, owned); err != nil {
					return err
				}
			}
		}
		if reconcile && complete {
			for key, row := range previous {
				if _, ok := rows[key]; ok {
					continue
				}
				_, err := os.Lstat(row.Path)
				if errors.Is(err, os.ErrNotExist) {
					if err := owned(); err != nil {
						return err
					}
					if err := retireCheckout(filepath.Join(directory, "clones", clone, "checkouts", key)); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
			}
		}
		slog.InfoContext(ctx, "repository observed", "repository", filepath.Base(directory), "clone", clone, "branches", len(branches), "checkouts", len(rows), "reconcile", reconcile)
		return nil
	})
	return directory, err
}
func retireCheckout(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "checkout.json" && entry.Name() != "access.json" {
			return fmt.Errorf("unrecognized checkout state retained: %s", root)
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(root)
}
func retireBranch(root string, owned func() error) error {
	if err := owned(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, "branch.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return os.Remove(root)
	}
	return nil
}
