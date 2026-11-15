package state

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/git/branch"
)

// PersistentDirectory returns the repository owner directory for configuration and provenance.
func PersistentDirectory(ctx context.Context, repo, home string) (string, error) {
	directory, _, _, err := stateLocation(ctx, repo, home)
	if err != nil {
		return "", err
	}
	return directory, nil
}

type BranchCreation struct {
	Branch     string  `json:"branch"`
	BaseRef    string  `json:"baseRef"`
	BaseCommit string  `json:"baseCommit"`
	Remote     string  `json:"remote"`
	CommonDir  string  `json:"commonDir"`
	CreatedAt  float64 `json:"createdAt"`
}

func creationPath(ctx context.Context, repo, name, home string) (string, string, CheckoutInfo, error) {
	directory, info, _, err := stateLocation(ctx, repo, home)
	return filepath.Join(directory, "clones", Digest(info.CommonDir), "branches", Part(name), "creation.json"), directory, info, err
}

// LoadBranchCreation reads explicit creation provenance, never inferring it from ancestry.
func LoadBranchCreation(ctx context.Context, repo, name, home string) (*BranchCreation, error) {
	path, _, info, err := creationPath(ctx, repo, name, home)
	if err != nil {
		return nil, err
	}
	var value BranchCreation
	found, err := Read(path, &value)
	if err != nil || !found {
		return nil, err
	}
	if value.Branch != name || value.CommonDir != info.CommonDir || value.BaseCommit == "" {
		return nil, fmt.Errorf("invalid branch creation identity")
	}
	return &value, nil
}
func recordBranchCreation(ctx context.Context, repo, name, ref, commit, remote, home string) error {
	path, directory, info, err := creationPath(ctx, repo, name, home)
	if err != nil {
		return err
	}
	value := BranchCreation{name, ref, commit, remote, info.CommonDir, float64(time.Now().UnixNano()) / 1e9}
	return writePersistent(ctx, directory, path, value)
}

// CreateBranch coordinates Git creation with durable provenance. Metadata failure never
// retracts a confirmed branch, including a branch whose carried changes need recovery.
func CreateBranch(ctx context.Context, repo string, options branch.Options, home string) (branch.Report, error) {
	report, err := branch.Create(ctx, repo, options)
	if report.BranchConfirmed && report.Action == "created" && report.BaseCommit != "" {
		if e := recordBranchCreation(ctx, repo, report.Branch, options.Base, report.BaseCommit, options.Remote, home); e != nil {
			report.Diagnostics = append(report.Diagnostics, branch.Diagnostic{Code: "state.creation_unavailable", Message: e.Error()})
			slog.WarnContext(ctx, "Branch created; provenance unavailable", "branch", report.Branch, "error", e)
		}
	}
	return report, err
}

// SaveWorktreePolicy stores repository-wide policy outside the expiring cache.
func SaveWorktreePolicy(ctx context.Context, repo, home string, policy WorktreePolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	directory, err := PersistentDirectory(ctx, repo, home)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "config.json")
	return withActivity(ctx, directory, func(ctx context.Context, lease *activityLease) error {
		config := map[string]any{}
		if _, err := Read(path, &config); err != nil {
			return err
		}
		if config == nil {
			return fmt.Errorf("configuration must be an object")
		}
		config["worktree"] = policy
		return Write(path, config, lease.owned)
	})
}

// Serialize first publication with expiry; the activity lease lives outside the disposable tree.
func writePersistent(ctx context.Context, directory, path string, value any) error {
	return withActivity(ctx, directory, func(ctx context.Context, lease *activityLease) error {
		return WithLock(ctx, path, func(owned func() error) error { return Write(path, value, owned) })
	})
}
