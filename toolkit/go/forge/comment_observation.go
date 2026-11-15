package forge

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/compforge/repokit/toolkit/go/state"
)

type observedComments struct {
	client *Client
	remote comment.Forge
}
type commentRecord struct {
	Version    int             `json:"version"`
	Connection string          `json:"connection"`
	ObservedAt time.Time       `json:"observedAt"`
	Comment    comment.Comment `json:"comment"`
}

// capture records confirmed remote facts, never authorizes an operation from Home.
// +spec=Deleting Home cannot change comment identity or reply relationships. A cache publication failure never makes a successful remote write retryable.
// +link=component://docs/home.md#comment-observations
func (c *observedComments) capture(ctx context.Context, key model.Key, rows []comment.Comment, replace bool, deleted string) {
	directory, prPath, connection := c.client.observationLocation(key)
	root := filepath.Join(filepath.Dir(prPath), "comments")
	err := state.RecordRepositoryAccess(ctx, c.client.config.Home, filepath.Base(directory))
	if err == nil {
		err = state.WithLock(ctx, root, func(owned func() error) error {
			if err := state.Write(filepath.Join(directory, "repository.json"), key.Repository, owned); err != nil {
				return err
			}
			if replace {
				entries, err := os.ReadDir(root)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				for _, entry := range entries {
					if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
						continue
					}
					if err := owned(); err != nil {
						return err
					}
					if err := os.Remove(filepath.Join(root, entry.Name(), connection+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
			}
			if deleted != "" {
				if err := owned(); err != nil {
					return err
				}
				if err := os.Remove(filepath.Join(root, state.Part(deleted), connection+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			now := time.Now().UTC()
			for _, row := range rows {
				if err := state.Write(filepath.Join(root, state.Part(row.ID), connection+".json"), commentRecord{1, connection, now, row}, owned); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		slog.WarnContext(ctx, "comment observation publication failed", "repository", key.Repository.Path, "number", key.Number, "error", err)
	}
}
func (c *observedComments) List(ctx context.Context, key model.Key) ([]comment.Comment, error) {
	rows, err := c.remote.List(ctx, key)
	if err == nil {
		c.capture(ctx, key, rows, true, "")
	}
	return rows, err
}
func (c *observedComments) Get(ctx context.Context, key model.Key, id comment.ID) (comment.Comment, error) {
	row, err := c.remote.Get(ctx, key, id)
	if err == nil {
		c.capture(ctx, key, []comment.Comment{row}, false, "")
	}
	return row, err
}
func (c *observedComments) Create(ctx context.Context, key model.Key, body string, anchor *comment.Anchor) (comment.Comment, error) {
	row, err := c.remote.Create(ctx, key, body, anchor)
	if err == nil {
		c.capture(ctx, key, []comment.Comment{row}, false, "")
	}
	return row, err
}
func (c *observedComments) Reply(ctx context.Context, key model.Key, id comment.ID, body string) (comment.Comment, error) {
	row, err := c.remote.Reply(ctx, key, id, body)
	if err == nil {
		c.capture(ctx, key, []comment.Comment{row}, false, "")
	}
	return row, err
}
func (c *observedComments) Update(ctx context.Context, key model.Key, id comment.ID, body string) (comment.Comment, error) {
	row, err := c.remote.Update(ctx, key, id, body)
	if err == nil {
		c.capture(ctx, key, []comment.Comment{row}, false, "")
	}
	return row, err
}
func (c *observedComments) Delete(ctx context.Context, key model.Key, id comment.ID) (comment.DeleteResult, error) {
	row, err := c.remote.Delete(ctx, key, id)
	if err == nil {
		c.capture(ctx, key, nil, false, id)
	}
	return row, err
}
func (c *observedComments) Resolve(ctx context.Context, key model.Key, id comment.ID, resolved bool) (comment.Comment, error) {
	row, err := c.remote.Resolve(ctx, key, id, resolved)
	if err == nil {
		c.capture(ctx, key, []comment.Comment{row}, false, "")
	}
	return row, err
}
