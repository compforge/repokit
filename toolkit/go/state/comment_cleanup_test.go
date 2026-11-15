package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
)

// +case=Live PR status gates lifecycle cleanup without a checkout; dry-run, active writers and errors preserve comments and registration.
func TestCleanCommentLifecycle(t *testing.T) {
	for _, scenario := range []string{"merged", "open", "failed", "locked", "dry", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			repository := common.Repository{Forge: common.Forge{Name: "gitlab"}, Path: "acme/repo"}
			key := RepositoryKey("gitlab", "acme/repo")
			directory := filepath.Join(home, "repositories", key)
			comments := filepath.Join(directory, "prs", "7", "comments")
			if err := RecordRepositoryAccess(ctx, home, key); err != nil {
				t.Fatal(err)
			}
			if err := Write(filepath.Join(directory, "repository.json"), repository, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(comments, "note%3A1"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(comments, "note%3A1", "connection.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			// A source link inside the disposable cache cannot expand the deletion boundary.
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "keep"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(source, filepath.Join(comments, "source")); err != nil {
				t.Fatal(err)
			}
			if scenario == "locked" {
				if err := os.MkdirAll(LockPath(comments)+".lock", 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expired" {
				activity, err := readActivity(directory)
				if err != nil {
					t.Fatal(err)
				}
				activity.LastAccessAt = float64(time.Now().Add(-40 * 24 * time.Hour).Unix())
				if err := Write(filepath.Join(directory, "access.json"), activity, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			previous, err := os.ReadFile(filepath.Join(directory, "access.json"))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			report, err := Clean(ctx, CleanOptions{Home: home, InactiveDays: 30, DryRun: scenario == "dry" || scenario == "expired", CurrentDirectory: home,
				MergedPR: func(_ context.Context, repo common.Repository, n int) (bool, error) {
					calls++
					if repo != repository || n != 7 {
						t.Fatal(repo, n)
					}
					if scenario == "failed" {
						return false, errors.New("unavailable")
					}
					return scenario != "open", nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			if (len(report.Errors) > 0) != (scenario == "failed") {
				t.Fatal(report)
			}
			if (calls == 0) != (scenario == "locked" || scenario == "expired") {
				t.Fatal(calls, report)
			}
			_, err = os.Stat(comments)
			if errors.Is(err, os.ErrNotExist) != (scenario == "merged") {
				t.Fatal(err, report)
			}
			current, err := os.ReadFile(filepath.Join(directory, "access.json"))
			if err != nil || string(current) != string(previous) {
				t.Fatal("maintenance renewed activity", err)
			}
			if _, err := os.Stat(filepath.Join(source, "keep")); err != nil {
				t.Fatal(err)
			}
			if scenario == "merged" || scenario == "dry" {
				if report.Actions[0].Action != "remove_comment" || report.EstimatedBytes != 2 {
					t.Fatal(report)
				}
			}
		})
	}
}
