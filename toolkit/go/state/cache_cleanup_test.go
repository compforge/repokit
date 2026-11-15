package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func putCache(t *testing.T, path string, value any) {
	t.Helper()
	if err := Write(path, value, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestCleanupPolicyConformance(t *testing.T) {
	var corpus struct {
		Cases []struct {
			Name             string
			Home, Repository map[string]any
			Expected         map[string]int
			Invalid          bool
		}
	}
	data, err := os.ReadFile("../../../conformance/cleanup-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, row := range corpus.Cases {
		t.Run(row.Name, func(t *testing.T) {
			home := t.TempDir()
			putCache(t, filepath.Join(home, "config.json"), row.Home)
			putCache(t, filepath.Join(home, "repositories", "fixture", "config.json"), row.Repository)
			p, err := LoadCleanupPolicy(home, "fixture")
			if (err != nil) != row.Invalid {
				t.Fatal(p, err)
			}
			if !row.Invalid {
				got := map[string]int{"repository": p.Repository.MaxCount, "pr": p.PR.MaxCount, "comment": p.Comment.MaxCount, "branch": p.Branch.MaxCount, "validation": p.Validation.MaxCount}
				if !reflect.DeepEqual(got, row.Expected) {
					t.Fatal(got, row.Expected)
				}
			}
		})
	}
}

// +case=Counts include every reply once across connections; eviction never leaves a partial comment collection.
func TestCommentBudgetCollectionAndConvergence(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repositories", "fixture")
	for number, size := range map[int]int{1: 3, 2: 2} {
		collection := filepath.Join(root, "prs", strconv.Itoa(number), "comments")
		putCache(t, filepath.Join(collection, "access.json"), map[string]any{"version": 1, "lastAccessAt": number})
		for i := 0; i < size; i++ {
			for _, connection := range []string{"a", "b"} {
				putCache(t, filepath.Join(collection, Part("note:"+strconv.Itoa(i)), connection+".json"), map[string]any{"version": 1})
			}
		}
	}
	policy := DefaultCleanupPolicy()
	policy.Comment.MaxCount = 3
	options := CacheCleanOptions{Home: home, RepositoryKey: "fixture", Policy: &policy, DryRun: true}
	preview, err := CleanComments(context.Background(), options)
	if err != nil || len(preview.Errors) > 0 || len(preview.Counts) != 1 || preview.Counts[0].Before != 5 || preview.Counts[0].After != 2 {
		t.Fatal(preview, err)
	}
	options.DryRun = false
	actual, err := CleanComments(context.Background(), options)
	if err != nil || len(actual.Actions) != 1 || actual.Actions[0].Path != preview.Actions[0].Path || actual.Actions[0].EstimatedBytes != preview.Actions[0].EstimatedBytes {
		t.Fatal(actual, preview, err)
	}
	if _, err := os.Stat(filepath.Join(root, "prs", "1", "comments")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	again, err := CleanComments(context.Background(), options)
	if err != nil || len(again.Actions) > 0 || again.Counts[0].After != 2 {
		t.Fatal(again, err)
	}
}

// +case=PR ownership composes comment removal without counting bytes twice; tracking and provenance survive cache pressure.
func TestRepositoryCleanupOwnershipAndTracking(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repositories", "fixture")
	putCache(t, filepath.Join(root, "config.json"), map[string]any{"cleanup": map[string]any{"pr": map[string]int{"max_count": 0}, "branch": map[string]int{"max_count": 0}}})
	for _, n := range []string{"1", "2"} {
		r := map[string]any{"version": 1}
		if n == "2" {
			r["scopeIds"] = []string{"tracked"}
		}
		putCache(t, filepath.Join(root, "prs", n, "a.json"), r)
		putCache(t, filepath.Join(root, "prs", n, "comments", "note%3A1", "a.json"), map[string]int{"version": 1})
	}
	branch := filepath.Join(root, "clones", "clone", "branches", "topic")
	putCache(t, filepath.Join(branch, "branch.json"), map[string]int{"version": 1})
	putCache(t, filepath.Join(branch, "creation.json"), map[string]string{"baseCommit": "retained"})
	putCache(t, filepath.Join(branch, "associations", "a.json"), map[string]int{"version": 1})
	options := CacheCleanOptions{Home: home, RepositoryKey: "fixture", DryRun: true}
	preview, err := CleanRepository(context.Background(), options)
	if err != nil || len(preview.Errors) > 0 {
		t.Fatal(preview, err)
	}
	var previewSize int64
	seen := map[string]bool{}
	for _, a := range preview.Actions {
		if a.Status != "planned" {
			continue
		}
		if seen[a.Path] {
			t.Fatal("duplicate action", a)
		}
		seen[a.Path] = true
		previewSize += a.EstimatedBytes
	}
	options.DryRun = false
	actual, err := CleanRepository(context.Background(), options)
	if err != nil || len(actual.Errors) > 0 {
		t.Fatal(actual, err)
	}
	var actualSize int64
	for _, a := range actual.Actions {
		if a.Status == "completed" {
			actualSize += a.EstimatedBytes
		}
	}
	if actualSize != previewSize {
		t.Fatal("dry run double counts", preview, actual)
	}
	for _, path := range []string{filepath.Join(root, "config.json"), filepath.Join(branch, "creation.json"), filepath.Join(root, "prs", "2", "a.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(path, err)
		}
	}
	if preview.Counts[0].OverBudget != 1 {
		t.Fatal(preview.Counts)
	}
}

// +case=Protected old candidates stay over budget without evicting the newest retained PR.
func TestProtectedPRDoesNotDisplaceNewerPR(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repositories", "fixture")
	for _, n := range []string{"1", "2"} {
		scopes := []string{}
		if n == "1" {
			scopes = []string{"watch"}
		}
		putCache(t, filepath.Join(root, "prs", n, "a.json"), map[string]any{"version": 1, "scopeIds": scopes})
		at := 1
		if n == "2" {
			at = 2
		}
		putCache(t, filepath.Join(root, "prs", n, "access.json"), map[string]any{"version": 1, "lastAccessAt": at})
	}
	p := DefaultCleanupPolicy()
	p.PR.MaxCount = 1
	report, err := CleanPullRequests(context.Background(), CacheCleanOptions{Home: home, RepositoryKey: "fixture", Policy: &p})
	if err != nil || len(report.Errors) > 0 || len(report.Counts) != 1 || report.Counts[0].After != 2 || report.Counts[0].OverBudget != 1 {
		t.Fatal(report, err)
	}
	for _, n := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(root, "prs", n, "a.json")); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"activity", "resources"} {
		if _, err := os.Stat(filepath.Join(home, "locks", kind, "fixture.lock")); !os.IsNotExist(err) {
			t.Fatal("lease survived operation", err)
		}
	}
}

// +case=Stale empty leases are reclaimed; live or unknown lock contents survive.
func TestCleanupLocksLifecycle(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "locks", "resources")
	for _, name := range []string{"stale", "live", "unknown"} {
		p := filepath.Join(root, name+".lock")
		os.MkdirAll(p, 0700)
		if name == "unknown" {
			os.WriteFile(filepath.Join(p, "owner"), []byte("evidence"), 0600)
		}
		if name != "live" {
			at := time.Now().Add(-time.Minute)
			os.Chtimes(p, at, at)
		}
	}
	for _, dry := range []bool{true, false} {
		actions, err := CleanLocks(context.Background(), home, "", dry)
		if err != nil || len(actions) != 1 || actions[0].Path != filepath.Join(root, "stale.lock") {
			t.Fatal(actions, err)
		}
	}
	for _, name := range []string{"live", "unknown"} {
		if _, err := os.Stat(filepath.Join(root, name+".lock")); err != nil {
			t.Fatal(err)
		}
	}
	actions, err := CleanLocks(context.Background(), home, "", false)
	if err != nil || len(actions) != 0 {
		t.Fatal(actions, err)
	}
}

// +case=Cleanup does not traverse a symlinked repository or lock collection into another tree.
func TestCleanupRejectsLinkedCollections(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(outside, "fixture", "prs", "1"), 0700)
	putCache(t, filepath.Join(outside, "fixture", "prs", "1", "a.json"), map[string]any{"version": 1})
	if err := os.Symlink(outside, filepath.Join(home, "repositories")); err != nil {
		t.Fatal(err)
	}
	_, err := CleanPullRequests(context.Background(), CacheCleanOptions{Home: home, RepositoryKey: "fixture", Expire: true})
	if err == nil {
		t.Fatal("followed linked repository collection")
	}
	if err := os.Symlink(outside, filepath.Join(home, "locks")); err != nil {
		t.Fatal(err)
	}
	_, err = CleanLocks(context.Background(), home, "", false)
	if err == nil {
		t.Fatal("followed linked lock collection")
	}
	if _, err := os.Stat(filepath.Join(outside, "fixture", "prs", "1", "a.json")); err != nil {
		t.Fatal(err)
	}
}
