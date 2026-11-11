package forge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
	repostate "github.com/compforge/repokit/toolkit/go/state"
)

type observedPR struct {
	PullRequest
	Repository      common.Repository `json:"repository"`
	Number          int               `json:"number"`
	DetailsComplete bool              `json:"detailsComplete,omitempty"`
}
type prObservation struct {
	Key           Key             `json:"key"`
	Snapshot      *observedPR     `json:"snapshot,omitempty"`
	ObservedAt    time.Time       `json:"observedAt"`
	AttemptedAt   time.Time       `json:"attemptedAt"`
	LastError     json.RawMessage `json:"lastError,omitempty"`
	NextRefreshAt *time.Time      `json:"nextRefreshAt,omitempty"`
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
func (c *Client) Load(ctx context.Context, repository string, number int, maxAge time.Duration) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.load_failed", "forge.load", repository) }()
	if maxAge < 0 || number < 1 || repository == "" {
		return PullRequest{}, errors.New("invalid PR observation request")
	}
	key := Key{c.Repository(repository), number}
	directory, path, connection := c.observationLocation(key)
	err = repostate.WithLock(ctx, path, func(owned func() error) error {
		previous, found, err := c.readObservation(key, path, connection)
		if err != nil {
			return err
		}
		reusable := found
		age := time.Since(previous.Observation.ObservedAt)
		if reusable && len(previous.Observation.LastError) == 0 && previous.Observation.Snapshot != nil && previous.Observation.Snapshot.DetailsComplete && age >= 0 && age < maxAge {
			result = previous.Observation.Snapshot.PullRequest
			result.Key = key
			return nil
		}
		result, err = c.Get(ctx, repository, number)
		if err != nil {
			return err
		}
		if reusable && previous.Observation.Snapshot != nil && previous.Observation.Snapshot.State == "merged" && result.State != "merged" {
			return errors.New("Forge contradicted a confirmed merged lifecycle")
		}
		return c.writeObservation(key, result, previous, directory, path, connection, owned)
	})
	return result, err
}

// observationLocation uses the Python/TS connection identity for equivalent provider endpoints.
// +spec=`Each connection owns its refresh and discovery record; other connections are never overwritten.`
func (c *Client) observationLocation(key Key) (directory, path, connection string) {
	endpoint := c.config.Host
	if c.config.APIHost != "" {
		endpoint = c.base
		suffix := "/api/v4"
		if c.config.Provider == "github" {
			suffix = "/api/v3"
		}
		if strings.HasPrefix(endpoint, "https://") && strings.HasSuffix(endpoint, suffix) {
			endpoint = strings.TrimPrefix(strings.TrimSuffix(endpoint, suffix), "https://")
		} else if endpoint == "https://api.github.com" && c.config.Host == "github.com" {
			endpoint = "github.com"
		}
	}
	connection = observationDigest([]string{key.Repository.Forge.Name, observationDigest([]string{c.config.Provider, key.Repository.Forge.Name, strings.ToLower(endpoint), c.config.Token})})
	directory = filepath.Join(c.config.Home, "repositories", repostate.RepositoryKey(key.Repository.Forge.Name, key.Repository.Path))
	path = filepath.Join(directory, "prs", strconv.Itoa(key.Number), "observations", connection+".json")
	return
}
func observationDigest(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return fmt.Sprintf("%x", sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))))
}
func (c *Client) readObservation(key Key, path, connection string) (prRecord, bool, error) {
	var record prRecord
	found, err := repostate.Read(path, &record)
	if err != nil || !found {
		return record, found, err
	}
	if record.Version != 1 || record.Connection != connection || record.Observation.Key != key {
		return record, false, errors.New("invalid PR observation identity or version")
	}
	return record, true, nil
}

func (c *Client) writeObservation(key Key, pr PullRequest, previous prRecord, directory, path, connection string, owned func() error) error {
	now := time.Now().UTC()
	observation := prObservation{Key: key, Snapshot: &observedPR{pr, key.Repository, key.Number, true}, ObservedAt: now, AttemptedAt: now}
	if pr.State != "merged" {
		next := now.Add(time.Minute)
		observation.NextRefreshAt = &next
	}
	scopes := previous.ScopeIDs
	if scopes == nil {
		scopes = []string{}
	}
	record := prRecord{Version: 1, Connection: connection, Observation: observation, ScopeIDs: scopes}
	if err := repostate.Write(filepath.Join(directory, "repository.json"), key.Repository, owned); err != nil {
		return err
	}
	return repostate.Write(path, record, owned)
}

// mutate publishes confirmed writes under the same lease as reads. Cache maintenance
// failures must not turn a successful remote mutation into a retryable remote failure.
// +spec=`Successful writes replace cached metadata and preserve discovery membership. Storage diagnostics never authorize repeating a remote write.`
func (c *Client) mutate(ctx context.Context, key Key, operation func() (PullRequest, error)) (PullRequest, error) {
	directory, path, connection := c.observationLocation(key)
	var result PullRequest
	err := repostate.WithLock(ctx, path, func(owned func() error) error {
		var err error
		result, err = operation()
		if err != nil {
			return err
		}
		previous, _, readErr := c.readObservation(key, path, connection)
		if readErr != nil {
			slog.WarnContext(ctx, "PR cache read failed after successful write", "repository", key.Repository.Path, "number", key.Number, "error", readErr)
		}
		if err := c.writeObservation(key, result, previous, directory, path, connection, owned); err != nil {
			// Remove a stale success if publication failed while we still own the lease.
			if owned() == nil {

				if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					slog.WarnContext(ctx, "PR cache invalidation failed", "repository", key.Repository.Path, "number", key.Number, "error", removeErr)
				}
			}
			slog.WarnContext(ctx, "PR cache publication failed after successful write", "repository", key.Repository.Path, "number", key.Number, "error", err)
		}
		return nil
	})
	return result, err
}
