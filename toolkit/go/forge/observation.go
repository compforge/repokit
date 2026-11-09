package forge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/internal/repostate"
)

type observedPR struct {
	PullRequest
	Repository common.Repository `json:"repository"`
	Number     int               `json:"number"`
}
type prObservation struct {
	Key           Key         `json:"key"`
	Snapshot      *observedPR `json:"snapshot,omitempty"`
	ObservedAt    time.Time   `json:"observedAt"`
	AttemptedAt   time.Time   `json:"attemptedAt"`
	NextRefreshAt *time.Time  `json:"nextRefreshAt,omitempty"`
}
type prRecord struct {
	Version     int           `json:"version"`
	Connection  string        `json:"connection"`
	Observation prObservation `json:"observation"`
	ScopeIDs    []string      `json:"scopeIds"`
	Failures    int           `json:"failures"`
}

// Load reuses PR metadata within maxAge or refreshes it from the configured Forge.
// Zero always fetches. Get stays a live read for write authorization and cleanup.
// +spec=`Connection scope includes endpoint, provider and credentials. Failed reads never return cached data as current. Merged lifecycle cannot regress on refresh; mutable metadata can refresh.`
// +link=component://docs/state.md
func (c *Client) Load(ctx context.Context, repository string, number int, maxAge time.Duration) (PullRequest, error) {
	if maxAge < 0 || number < 1 || repository == "" {
		return PullRequest{}, errors.New("invalid PR observation request")
	}
	if c.config.StateDir == "" {
		return c.Get(ctx, repository, number)
	}
	key := Key{c.Repository(repository), number}
	directory := filepath.Join(c.config.StateDir, "repositories", repostate.RepositoryKey(key.Repository.Forge.Name, repository))
	path := filepath.Join(directory, "prs", strconv.Itoa(number), "observation.json")
	scope, _ := json.Marshal([]string{c.config.Provider, c.base, c.config.Token})
	connection := fmt.Sprintf("%x", sha256.Sum256(scope))
	var result PullRequest
	err := repostate.WithLock(ctx, path, func(owned func() error) error {
		var previous prRecord
		found, err := repostate.Read(path, &previous)
		if err != nil {
			return err
		}
		if found && previous.Version != 1 {
			return errors.New("invalid PR observation version")
		}
		reusable := found && previous.Connection == connection
		if reusable && previous.Observation.Key != key {
			return errors.New("PR observation identity mismatch")
		}
		age := time.Since(previous.Observation.ObservedAt)
		if reusable && previous.Observation.Snapshot != nil && age >= 0 && age < maxAge {
			result = previous.Observation.Snapshot.PullRequest
			return nil
		}
		result, err = c.Get(ctx, repository, number)
		if err != nil {
			return err
		}
		if reusable && previous.Observation.Snapshot != nil && previous.Observation.Snapshot.State == "merged" && result.State != "merged" {
			return errors.New("Forge contradicted a confirmed merged lifecycle")
		}
		now := time.Now().UTC()
		observation := prObservation{Key: key, Snapshot: &observedPR{result, key.Repository, key.Number}, ObservedAt: now, AttemptedAt: now}
		if result.State != "merged" {
			next := now.Add(time.Minute)
			observation.NextRefreshAt = &next
		}
		record := prRecord{Version: 1, Connection: connection, Observation: observation, ScopeIDs: []string{}}
		if err := repostate.Write(filepath.Join(directory, "repository.json"), key.Repository, owned); err != nil {
			return err
		}
		return repostate.Write(path, record, owned)
	})
	return result, err
}
