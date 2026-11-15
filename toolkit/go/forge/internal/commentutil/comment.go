// Package commentutil shares validation and bounded transport mechanics, not provider semantics.
package commentutil

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

func Invalid(message string) error {
	return &errs.Error{Code: "forge.invalid_argument", Message: message}
}
func Unsupported(message string) error {
	return &errs.Error{Code: "forge.unsupported_operation", Message: message}
}
func Key(c *transport.Client, key model.Key) error {
	if key.Number <= 0 || key.Repository.Path == "" || key.Repository != c.Repository(key.Repository.Path) {
		return Invalid("comment requires a PR identity on this Forge")
	}
	return nil
}
func Body(body string) error {
	if strings.TrimSpace(body) == "" {
		return Invalid("comment body must not be empty")
	}
	return nil
}
func ValidateAnchor(a *comment.Anchor) error {
	if a == nil {
		return nil
	}
	if a.Path == "" || a.Revision == "" || (a.Side != "LEFT" && a.Side != "RIGHT") {
		return Invalid("anchor requires path, revision and LEFT/RIGHT side")
	}
	if a.Line != nil && *a.Line <= 0 || a.StartLine != nil && (a.Line == nil || *a.StartLine <= 0) {
		return Invalid("anchor lines must be positive")
	}
	if a.StartSide != "" && a.StartSide != "LEFT" && a.StartSide != "RIGHT" {
		return Invalid("invalid start side")
	}
	return nil
}
func Pages[T any](ctx context.Context, c *transport.Client, path string) ([]T, error) {
	result := []T{}
	for page := 1; page <= 100; page++ {
		var rows []T
		if err := c.Request(ctx, "GET", path+"?per_page=100&page="+strconv.Itoa(page), nil, &rows); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, fmt.Errorf("invalid comment inventory at %s", path)
		}
		result = append(result, rows...)
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("comment inventory exceeds 100 pages: %s", path)
}
func Find(comments []comment.Comment, id string) (comment.Comment, error) {
	for _, c := range comments {
		if c.ID == id {
			return c, nil
		}
	}
	return comment.Comment{}, &errs.Error{Code: "forge.not_found", Message: "comment not found in the specified PR", Subject: id}
}
func Confirm(c comment.Comment, body, id string, err error) (comment.Comment, error) {
	if err != nil {
		return c, transport.UnknownFailure(err)
	}
	if c.Body != body || id != "" && c.ID != id {
		return c, transport.UnknownFailure(fmt.Errorf("Forge did not confirm requested comment identity/body"))
	}
	slog.Info("comment write confirmed", "repository", c.PR.Repository.Path, "pr", c.PR.Number, "comment", c.ID)
	return c, nil
}
